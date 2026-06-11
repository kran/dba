# dba

`dba` is an immutable, chainable SQL builder and execution engine built on top of `sqlx`.

## Quick Example

```go
q := dba.New(db)

// Define a reusable base query — immutable, safe to share
base := q.Add("SELECT ${F:u.id, u.name, u.email} FROM users u").
    Add("JOIN orders o ON o.user_id = u.id WHERE 1 = 1"). //"1 = 1" for empty where, ugly but useful
    AddIf(req.Status != "", "AND u.status = #{1}", req.Status).
    AddIf(req.MinAge > 0, "AND u.age >= #{1}", req.MinAge).
    Add("ORDER BY u.id DESC")

// Paginated list — F is swapped to COUNT(1) for total, then LIMIT/OFFSET for data
users, total, err := dba.Page[User](base, req.Page, req.Size)

// Reuse the same base for a different purpose — original is untouched
uid, _, err := dba.Scalar[int64](base.Var("F", "u.id").Add("LIMIT 1"))

// CRUD with hooks — struct methods run automatically before insert
type User struct {
    ID        int       `db:"id,omitempty"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at"`
}
func (u *User) BeforeCreate() error {
    u.CreatedAt = time.Now()
    return nil
}

dao := dba.NewDao[User](q, "users")
id, err := dao.Create(User{Name: "alice"})          // hook sets created_at
dao.Update(map[string]any{"name": "bob"}, "id = #{1}", id) // map skips hooks
user, err := dao.GetByID(id)                         // returns *User, nil when not found
```

---

## Design Philosophy

1. **Immutability**: Every method that mutates state (`Add`, `Var`, `AddIf`) returns a new copy. The original instance is never modified, making it safe for concurrent use and reuse across requests.
2. **SQL complexity stays in SQL**: No semantic builder chains — you write SQL, `dba` handles parameter binding, identifier quoting, and dialect translation.
3. **Explicit over implicit**: Zero values are included by default. Fields are only omitted from `INSERT`/`UPDATE` when tagged `db:"name,omitempty"` and the value is zero.
4. **Deterministic output**: Columns from maps and structs are sorted lexicographically, ensuring consistent SQL for query plan cache reuse.
5. **Delegation**: Struct mapping and driver interaction are delegated to `sqlx` and `reflectx`. `dba` doesn't reinvent them.

---

## Installation

```bash
go get codeberg.org/kran/dba
```

---

## Initialization

```go
import "codeberg.org/kran/dba"

dbx, _ := sqlx.Connect("postgres", "user=foo dbname=bar sslmode=disable")
q := dba.New(dbx)
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
q = q.Format(dba.DollarFormat)
```

Built-in quoters: `dba.AnsiQuoter`, `dba.MySQLQuoter`.
Built-in formats: `dba.QuestionMarkFormat`, `dba.DollarFormat`.

---

## Core Builder

### Macro syntax

| Syntax | Behavior |
|--------|----------|
| `#{1}` | Bind parameter by positional index (1-based) |
| `#{name}` | Bind parameter from named arg (last arg as struct/map) |
| `${key:default}` | Variable expansion — uses `Var(key, ...)` if set, otherwise `default` |
| `@{1}` / `@{name}` | Identifier quoting (table/column names);  |
| `!{1}` / `!{name}` | Raw interpolation (injection risk); |
| `##{...}` | Escape — double the prefix to output literally (`$${` → `${`, `@@{` → `@{`, `!!{` → `!{`) |

Slices passed to `#{}` are auto-expanded: `#{1}` with `[]int{1,2,3}` → `$1, $2, $3`.

`@{}` and `!{}` return an error if the resolved value is `nil` or no value found.

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

```

### Var — declarative variable slots

`Var(name, query, args...)` registers a named variable. It doesn't produce any SQL output by itself — it only takes effect when `${name}` or `${name:default}` appears in the query. This makes it **position-independent** and **purely declarative**.

```go
// Basic: Var overrides the inline default
base := q.Add("SELECT ${F:*} FROM users ORDER BY id")
base.Var(dba.F, "id, name").ToSQL() // SELECT id, name FROM users ORDER BY id
base.ToSQL()                          // SELECT * FROM users ORDER BY id (uses default)

// Position doesn't matter — these are equivalent
q.Add("SELECT ${F:*} FROM t").Var(dba.F, "id")
q.Var(dba.F, "id").Add("SELECT ${F:*} FROM t")

// Multiple slots with defaults
q.Add("SELECT ${F:*} FROM users ${where:} ${order:ORDER BY id} ${limit:}").
    Var("where", "WHERE status = #{1}", "active").
    Var("limit", "LIMIT #{1}", 20)
// SELECT * FROM users WHERE status = $1 ORDER BY id LIMIT $2

// Same Var referenced multiple times
q.Add("SELECT ${F:*} FROM t WHERE ${F:*} IS NOT NULL").
    Var(dba.F, "name")
// SELECT name FROM t WHERE name IS NOT NULL

// Immutable: Var returns a new copy, base is unchanged
base  := q.Add("SELECT ${F:*} FROM users")
count := base.Var(dba.F, "COUNT(1)")  // for counting
data  := base.Add("LIMIT 10")           // for data query
// base, count, data are three independent queries

// Var with parameter binding — each Var has its own args namespace
q.Add("SELECT ${F:*} FROM users ${filter}").
    Var("filter", "WHERE age > #{1} AND city = #{2}", 18, "NYC")
// SELECT * FROM users WHERE age > $1 AND city = $2

// Nested Var — a Var's SQL can reference other Vars
q.Add("SELECT * FROM t ${filter}").
    Var("filter", "WHERE id = #{1} ${extra}", 5).
    Var("extra", "AND active = #{1}", true)
// SELECT * FROM t WHERE id = $1 AND active = $2
```

**Key properties:**
- `${key:default}` — if Var not set, uses the text after `:` as fallback (plain text only, no nested `{}`)
- `${key}` — if Var not set and no default, outputs nothing
- Each Var has its own args namespace — `#{1}` in different Vars never conflict
- Var is the mechanism behind `Page` (swaps fields for `COUNT(1)`) and `Expr` (raw SQL in Insert/Update)

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

Use `dba.Expr` to embed raw SQL expressions in `Insert`/`Update` values. Expr values are expanded through the build engine via `Var`, so `#{}` macros work naturally inside them:

```go
q.Update("stats", map[string]any{
    "views": dba.Expr("views + 1"),
    "score": dba.Expr("score + #{1}", 10),
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
count, _, err := dba.Scalar[int64](q.Add("SELECT COUNT(1) FROM users"))

// Pagination — query must contain ${F:...} or use Var(dba.F, ...)
items, total, err := dba.Page[User](
    q.Add("SELECT ${F:*} FROM users WHERE age > #{1} ORDER BY id", 18),
    page, size,
)

// Complex join query with Page
items, total, err := dba.Page[User](
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
err := q.Transaction(func(tx *dba.SQL) error {
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
q = q.Use(func(next dba.ExecFunc) dba.ExecFunc {
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
q = q.Use(dba.LogMiddleware(slog.Default(), 200*time.Millisecond))
```

---

## Generic DAO

`Dao[T]` provides single-table CRUD with dialect-aware `Create`.

```go
type User struct {
    ID   int    `db:"id,omitempty"`
    Name string `db:"name"`
}

dao := dba.NewDao[User](q, "users")
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

// CreateRaw returns *dba.SQL for chaining (e.g. add RETURNING, ON CONFLICT)
dao.CreateRaw(user).Add("ON CONFLICT DO NOTHING").Exec()

// Queries
items, err  := dao.List("val > #{1}", 10)
items, err   = dao.All()
count, err  := dao.Count("val > #{1}", 10)
exists, err := dao.Exists("name = #{1}", "alice")

// Access underlying dba.SQL for custom queries
sum, _, err := dba.Scalar[int64](dao.Q().Add("SELECT SUM(val) FROM users"))
```

### Cross-DAO transactions

```go
err = q.Transaction(func(tx *dba.SQL) error {
    userID, err := userDao.WithTx(tx).Create(User{Name: "alice"})
    if err != nil {
        return err
    }
    _, err = orderDao.WithTx(tx).Create(Order{UserID: int(userID), Product: "widget"})
    return err
})
```
