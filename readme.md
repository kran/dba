# sqlo

`sqlo` is an immutable, chainable SQL builder and execution engine built on top of `sqlx`.

## Design Philosophy

1. **Immutability**: Every method that mutates state (`Add`, `Var`, `AddIf`) returns a new copy. The original instance is never modified, making it safe for concurrent use and reuse across requests.
2. **SQL complexity stays in SQL**: No semantic builder chains — you write SQL, `sqlo` handles parameter binding, identifier quoting, and dialect translation.
3. **Explicit over implicit**: Zero values are included by default. Fields are only omitted from `INSERT`/`UPDATE` when tagged `db:"name,omitempty"` and the value is zero.
4. **Deterministic output**: Columns from maps and structs are sorted lexicographically, ensuring consistent SQL for query plan cache reuse.
5. **Delegation**: Struct mapping and driver interaction are delegated to `sqlx` and `reflectx`. `sqlo` doesn't reinvent them.

---

## Installation

```bash
go get codeberg.org/kran/sqlo
```

---

## Initialization

```go
import "codeberg.org/kran/sqlo"

dbx, _ := sqlx.Connect("postgres", "user=foo dbname=bar sslmode=disable")
q := sqlo.New(dbx)
```

`New` auto-detects the driver:
- **PostgreSQL/pgx/DuckDB**: ANSI double-quote quoting + `$1` placeholders
- **MySQL**: Backtick quoting + `?` placeholders
- **Others (SQLite etc.)**: ANSI double-quote quoting + `?` placeholders

Override with `Quoter` or `Format`:

```go
// MSSQL uses [bracket] quoting
mssqlQuoter := func(s string) string {
    return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}
q = q.Quoter(mssqlQuoter)

// Override placeholder format
q = q.Format(sqlo.DollarFormat)
```

Built-in quoters: `sqlo.AnsiQuoter`, `sqlo.MySQLQuoter`.
Built-in formats: `sqlo.QuestionMarkFormat`, `sqlo.DollarFormat`.

---

## Core Builder

### Macro syntax

| Syntax | Behavior |
|--------|----------|
| `#{1}` | Bind parameter by positional index (1-based) |
| `#{name}` | Bind parameter from named arg (last arg as struct/map) |
| `${key:default}` | Variable expansion — uses `Var(key, ...)` if set, otherwise `default` |
| `@{1}` / `@{name}` | Identifier quoting (table/column names); literal fallback if no arg |
| `!{1}` / `!{name}` | Raw interpolation (injection risk); literal fallback if no arg |
| `\#{...}` | Escape — outputs `#{...}` literally (also works for `\${`, `\@{`, `\!{`) |

Slices passed to `#{}` are auto-expanded: `#{1}` with `[]int{1,2,3}` → `$1, $2, $3`.

`@{}` and `!{}` return an error if the resolved value is `nil`.

### Methods

- `Add(query, args...)` — append a SQL fragment
- `AddIf(cond, query, args...)` — conditional append
- `Var(name, query, args...)` — register a named variable, expanded when `${name}` appears

```go
// Positional
q.Add("SELECT * FROM users WHERE status = #{1}", "active")

// Named (map or struct as last arg)
q.Add("WHERE id = #{id} AND name = #{name}", map[string]any{
    "id":   42,
    "name": "alice",
})

// Var: define a reusable slot
base := q.Add("SELECT ${F:*} FROM users ORDER BY id")
base.Var(sqlo.F, "id, name").ToSQL() // SELECT id, name FROM users ORDER BY id
base.ToSQL()                          // SELECT * FROM users ORDER BY id (default)

// Inline default with Var override
q.Add("SELECT ${F:*} FROM users ${order:ORDER BY id}").
    Var("order", "ORDER BY name DESC")
```

---

## DML Helpers

Generate `SELECT`, `INSERT`, `UPDATE`, `DELETE`. Column order is sorted. `omitempty` zero values are excluded.

```go
type User struct {
    ID   int    `db:"id,omitempty"` // excluded when 0
    Name string `db:"name"`
}

// Select: shorthand, implicitly uses ${F:*} (compatible with Page)
q.Select("users", "age > #{1} ORDER BY age", 18)
// SELECT "id", "name" FROM "users" WHERE age > $1 ORDER BY age

q.Insert("users", User{Name: "alice"})
// INSERT INTO "users" ("name") VALUES ($1)

q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 42)
// UPDATE "users" SET "name"=$1 WHERE id = $2

q.Delete("users", "id = #{1}", 42)
// DELETE FROM "users" WHERE id = $1
```

### Expr — raw SQL values

Use `sqlo.NewExpr` to embed raw SQL expressions in `Insert`/`Update` values. Expr values are expanded through the build engine via `Var`, so `#{}` macros work naturally inside them:

```go
q.Update("stats", map[string]any{
    "views": sqlo.NewExpr("views + 1"),
    "score": sqlo.NewExpr("score + #{1}", 10),
}, "id = #{1}", 1)
// UPDATE "stats" SET "score"=score + $1, "views"=views + 1 WHERE
// id = $2
```

### Batch — multi-row value groups

`Batch` generates `(?,?), (?,?), ...` and appends it to the current query:

```go
users := [][]any{
    {1, "alice"},
    {2, "bob"},
    {3, "carol"},
}
// Bulk INSERT
q.Add("INSERT INTO users (id, name) VALUES").Batch(users).Exec()
// INSERT INTO users (id, name) VALUES ($1,$2), ($3,$4), ($5,$6)

// Multi-column IN
q.Add("SELECT * FROM users WHERE (id, name) IN (").Batch(users).Add(")")
```

---

## Terminal Methods

All terminal methods go through the middleware chain.

- `Get(dest) (bool, error)` — single row; returns `(false, nil)` when not found
- `List(dest) error` — multiple rows into a slice pointer
- `Exec() (sql.Result, error)` — non-query statements
- `Rows() (*sqlx.Rows, error)` — raw cursor for streaming
- `ToSQL() (string, []any, error)` — debug, no execution

---

## Generic Utilities

```go
// Single scalar value
count, _, err := sqlo.Scalar[int64](q.Add("SELECT COUNT(1) FROM users"))

// Pagination — query must contain ${F:...} or use Var(sqlo.F, ...)
items, total, err := sqlo.Page[User](
    q.Add("SELECT ${F:*} FROM users WHERE age > #{1} ORDER BY id", 18),
    page, size,
)

// Complex join query with Page
items, total, err := sqlo.Page[User](
    q.Add("SELECT ${F:u.id, u.name, COUNT(o.id) AS orders} FROM users u").
        Add("LEFT JOIN orders o ON u.id = o.user_id").
        AddIf(name != "", "WHERE u.name = #{1}", name).
        Add("GROUP BY u.id, u.name").
        Add("ORDER BY u.id DESC"),
    page, size,
)
```

`Page` substitutes `F` with `COUNT(1)` for the total query, then adds `LIMIT/OFFSET` for data. Short-circuits when `total == 0`.

---

## Transaction Management

```go
err := q.Transaction(func(tx *sqlo.Sqlo) error {
    _, err := tx.Insert("users", User{Name: "alice"}).Exec()
    if err != nil {
        return err // triggers rollback
    }
    _, err = tx.Update("stats", map[string]any{"count": 1}, "id = #{1}", 1).Exec()
    return err // nil triggers commit
})
```

Panic inside the closure also triggers rollback. Nested `Begin` returns an error.

---

## Middleware

```go
type ExecFunc func(ctx context.Context, query string, args []any) (any, error)
type Middleware func(next ExecFunc) ExecFunc
```

All terminal methods go through `Use`-registered middleware (onion model):

```go
q = q.Use(func(next sqlo.ExecFunc) sqlo.ExecFunc {
    return func(ctx context.Context, query string, args []any) (any, error) {
        start := time.Now()
        result, err := next(ctx, query, args)
        log.Printf("query took %s", time.Since(start))
        return result, err
    }
})
```

Built-in `LogMiddleware` with slow query detection (not attached by default):

```go
q = q.Use(sqlo.LogMiddleware(slog.Default(), 200*time.Millisecond))
```

---

## Generic DAO

`Dao[T]` provides single-table CRUD with dialect-aware `Create`.

```go
type User struct {
    ID   int    `db:"id,omitempty"`
    Name string `db:"name"`
}

dao := sqlo.NewDao[User](q, "users")
dao = dao.PrimaryKey("id") // default is "id", cloned
dao = dao.TableName("users_1") // change table name, for table partitions, cloned

// Create returns the generated PK
id, err := dao.Create(User{Name: "alice"})

// Get returns *T — nil when not found, no error
user, err := dao.GetByID(id)
user, err  = dao.Get("name = #{1}", "alice")

// Update/Delete return affected rows
affected, err := dao.Update(map[string]any{"name": "bob"}, "id = #{1}", id)
affected, err  = dao.Delete("id = #{1}", id)

// CreateRaw returns *Sqlo for chaining (e.g. add RETURNING, ON CONFLICT)
dao.CreateRaw(user).Add("ON CONFLICT DO NOTHING").Exec()

// Queries
items, err  := dao.List("val > #{1}", 10)
items, err   = dao.All()
count, err  := dao.Count("val > #{1}", 10)
exists, err := dao.Exists("name = #{1}", "alice")

// Access underlying Sqlo for custom queries
sum, _, err := sqlo.Scalar[int64](dao.Q().Add("SELECT SUM(val) FROM users"))
```

### Cross-DAO transactions

```go
err = q.Transaction(func(tx *sqlo.Sqlo) error {
    userID, err := userDao.WithTx(tx).Create(User{Name: "alice"})
    if err != nil {
        return err
    }
    _, err = orderDao.WithTx(tx).Create(Order{UserID: int(userID), Product: "widget"})
    return err
})
```
