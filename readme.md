# sqlo

`sqlo` is an immutable, chainable SQL builder and execution engine built on top of `sqlx`.

## Design Philosophy

1. **Immutability**: Every method that mutates state (`Add`, `Mark`, `AddIf`) returns a new copy. The original instance is never modified, making it safe for concurrent use and reuse across requests.
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

`New` auto-detects the driver: MySQL uses backtick quoting, everything else uses ANSI double-quote. For other dialects, override with `Quoter`:

```go
// MSSQL uses [bracket] quoting
mssqlQuoter := func(s string) string {
    return "[" + strings.ReplaceAll(s, "]", "]]") + "]"
}
q = q.Quoter(mssqlQuoter)
// @{table} → [table], @{column} → [column]
```

Built-in quoters are exported for convenience: `sqlo.AnsiQuoter`, `sqlo.MySQLQuoter`.

---

## Core Builder

### Macro syntax

| Syntax | Behavior |
|--------|----------|
| `#{1}` | Bind parameter by positional index (1-based) |
| `#{name}` | Bind parameter from named arg (last arg as struct/map) |
| `@{1}` / `@{name}` | Identifier quoting (table/column names) |
| `!{1}` / `!{name}` | Raw interpolation (injection risk — use carefully) |
| `??` | Literal `?` (for PostgreSQL JSONB operators) |

### Methods

- `Add(query, args...)` — append a SQL fragment, parse macros
- `AddIf(cond, query, args...)` — conditional append
- `Mark(name, query, args...)` — named slot, can be overridden later

```go
// Positional
q.Add("SELECT * FROM users WHERE status = #{1}", "active")

// Named (map or struct as last arg)
q.Add("SELECT * FROM @{table} WHERE id = #{id}", map[string]any{
    "table": "users",
    "id":    42,
})

// Mark: reserve FIELD slot for later override (e.g. Page)
q.Add("SELECT").Mark(sqlo.F, "id, name").Add("FROM users")
```

---

## DML Helpers

Generate `SELECT`, `INSERT`, `UPDATE`, `DELETE`. Column order is sorted. `omitempty` zero values are excluded.

```go
type User struct {
    ID   int    `db:"id,omitempty"` // excluded when 0
    Name string `db:"name"`
}

// Select: shorthand for simple queries, implicitly marks F="*" (compatible with Page)
q.Select("users", "age > #{1} ORDER BY age", 18)
// SELECT * FROM "users" WHERE age > ? ORDER BY age

q.Insert("users", User{Name: "alice"})
// INSERT INTO "users" ("name") VALUES (?)

q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 42)
// UPDATE "users" SET "name"=? WHERE id = ?

q.Delete("users", "id = #{1}", 42)
// DELETE FROM "users" WHERE id = ?
```

### Expr — raw SQL values

Use `sqlo.NewExpr` to embed raw SQL expressions in `Insert`/`Update` values:

```go
q.Update("stats", map[string]any{
    "views": sqlo.NewExpr("views + 1"),
    "score": sqlo.NewExpr("score + #{1}", 10),
}, "id = #{1}", 1)
// UPDATE "stats" SET "score"=score + ?, "views"=views + 1 WHERE
// id = ?
```

### Batch — multi-row value groups

`Batch` generates `(?,?), (?,?), ...` and appends it to the current query. Works for bulk INSERT and PostgreSQL multi-column `IN`:

```go
users := [][]any{
    {1, "alice"},
    {2, "bob"},
    {3, "carol"},
}
// Bulk INSERT
q.Add("INSERT INTO users (id, name) VALUES").Batch(users).Exec()
// INSERT INTO users (id, name) VALUES (?,?), (?,?), (?,?)

// PG multi-column IN
q.Add("SELECT * FROM users WHERE (id, name) IN (").Batch(users).Add(")")
// SELECT * FROM users WHERE (id, name) IN ((?,?), (?,?), (?,?))
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

// Pagination — query must use Mark(sqlo.F, ...) for SELECT fields
// sql.Select() has the `sqlo.F` mark implicitly, for simple queries
items, total, err := sqlo.Page[User](q.Select("users", "age > ? order by age", 18), page, size)

// complicate queries
items, total, err := sqlo.Page[User](
    q.Add("SELECT").Mark(sqlo.F, "u.*, o.count").
        Add("FROM users u LEFT JOIN orders o ON u.id = o.user_id").
        AddIf(name != "", "WHERE u.name = #{1}", name).
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
