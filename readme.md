# dba

`dba` is an immutable, chainable SQL builder for `sqlx`. No ORM, no code generation — you write SQL, it handles the plumbing.

## Quick Example

```go
q := dba.New(db)

// One base query, three uses — none of them mutate the original
base := q.Add("SELECT ${F:*} FROM users ${where:} ${order:ORDER BY id}")

// Use 1: filtered list with pagination
filter := base.
    Var("where", "WHERE status = #{1} AND age >= #{2}", "active", 18).
    Var("order", "ORDER BY created_at DESC")

// Use 2: count total for the same filter — Var overrides F to COUNT(1)
total, _, _ := dba.Scalar[int64](filter.Var(dba.F, "COUNT(1)"))

// Use 3: get just one column for a dropdown
ids := []int64{}
filter.Var(dba.F, "DISTINCT id").List(&ids)
```

**What just happened:**
- `${F:*}` and `${where:}` are named slots — fill them with `Var`, leave them empty to use the inline default (`*` and nothing)
- `dba.Scalar` and `dba.Page` reuse the same query for count + data without string manipulation
- `filter` is still untouched after all three calls — immutable, safe to pass around

---

## Why Not Just sqlx?

```go
// sqlx — string wrangling, manual IN expansion, error-prone
query := "SELECT * FROM users WHERE status = ?"
args := []any{"active"}
if name != "" {
    query += " AND name = ?"
    args = append(args, name)
}
query, args, _ := sqlx.In("SELECT * FROM users WHERE id IN (?)", ids)
// → "SELECT * FROM users WHERE id IN (?,?,?)" with 3 args

// dba — same thing, cleaner
q.Add("SELECT * FROM users WHERE status = #{1}", "active").
    AddIf(name != "", "AND name = #{1}", name).
    Add("AND id IN (#{1})", ids) // auto-expands slices
```

| Pain point | Raw sqlx | dba |
|------------|----------|-----|
| `IN (?)` expansion | `sqlx.In()` in a separate call | `#{1}` auto-detects slices |
| Conditional clauses | String concat + manual arg spread | `AddIf(cond, ...)` |
| Count + data pagination | Two manually maintained SQL strings | `Page[T](base, page, size)` |
| Identifier quoting | Manual per dialect | `@{1}` auto-quotes for PG/MySQL/SQLite |
| Struct hooks (timestamps) | Manual before every insert | `BeforeCreate() error` interface |

---

## Core Builder

### Macro syntax

| Syntax | What it does | Example |
|--------|-------------|---------|
| `#{1}` | Positional parameter | `"WHERE id = #{1}"` → `"WHERE id = $1"` |
| `#{name}` | Named parameter from map/struct | `"WHERE id = #{id}"` + `map["id"]` |
| `${key:default}` | Fillable slot with fallback | `"${order:ORDER BY id}"` → overridable |
| `@{1}` / `@{name}` | Dialect-aware quoting | `"SELECT @{1}"` → `"SELECT \"name\""` |
| `!{1}` / `!{name}` | Raw text injection (use sparingly) | `"ORDER BY !{1}"` → `"ORDER BY id DESC"` |
| `##{text}` | Escape — output `#{text}` literally | `"##{1}"` → literal `"#{1}"` |

Slices passed to `#{}` auto-expand: `[]int{1,2,3}` → `$1, $2, $3`. No more `sqlx.In`.

### Methods

```go
q.Add("SELECT * FROM users WHERE status = #{1}", "active")          // positional
q.AddIf(minAge > 0, "AND age >= #{1}", minAge)                      // conditional
q.Add("WHERE (name, dept) = (#{1}, #{2})", "alice", "engineering") // named from map
```

### Var — declarative slots

Slots let you delay decisions about what to SELECT or filter on. They're the mechanism behind `Page` and `Expr`.

```go
// Basic: override the inline default
q.Add("SELECT ${F:*} FROM users").Var(dba.F, "id, name").ToSQL()
// → SELECT id, name FROM users

// Multiple optional clauses
q.Add("SELECT * FROM users ${where:} ${order:ORDER BY id} ${limit:}").
    Var("where", "WHERE status = #{1}", "active").
    Var("limit", "LIMIT #{1}", 20)
// → SELECT * FROM users WHERE status = $1 ORDER BY id LIMIT $2

// Immutable — each Var returns a new copy
base   := q.Add("SELECT ${F:*} FROM users")
count  := base.Var(dba.F, "COUNT(1)")   // for counting
data   := base.Add("LIMIT 10")           // for listing
// base, count, data are three independent queries
```

`${key:default}` — uses `default` when Var not set. `${key}` with no Var and no default outputs nothing.

---

## DML Helpers

Columns are sorted lexicographically for stable SQL. `omitempty` zero values are excluded.

```go
type User struct {
    ID        int    `db:"id,omitempty"`
    Name      string `db:"name"`
    CreatedAt string `db:"created_at"`
}

q.Insert("users", User{Name: "alice"})
// INSERT INTO "users" ("name") VALUES ($1)  — ID and CreatedAt omitted

q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 42)
// UPDATE "users" SET "name"=$1 WHERE id = $2

q.Delete("users", "id = #{1}", 42)
// DELETE FROM "users" WHERE id = $1
```

`${I}` is an optional slot between `INSERT` and `INTO` — use `Var(dba.I, "OR IGNORE")` to generate `INSERT OR IGNORE INTO`.

### Expr — raw SQL in values

```go
q.Update("stats", map[string]any{
    "views": dba.Expr("views + 1"),
    "score": dba.Expr("score + #{1}", 10),
}, "id = #{1}", 1)
// UPDATE "stats" SET "score"=score + $1, "views"=views + 1 WHERE id = $2
```

### BatchInsert — struct slices to bulk INSERT

```go
users := []User{
    {Name: "alice"},
    {Name: "bob"},
    {Name: "carol"},
}
q.BatchInsert("users", anySlice(users)).Exec()
// INSERT INTO "users" ("name") VALUES ($1), ($2), ($3)
```

### sql.Null* types

NullString/NullInt64 are treated as atomic columns. With `omitempty`, only the zero value (`Valid=false` + zero inner value) is omitted:

```go
type Profile struct {
    Bio sql.NullString `db:"bio,omitempty"`
}
// NullString{Valid: false} → omitted (zero value)
// NullString{Valid: true, String: ""} → kept (user set empty string)
// NullString{Valid: true, String: "hello"} → kept
```

---

## Pagination

```go
q := dba.New(db).Add("SELECT ${F:*} FROM users ${where:}").
    AddIf(status != "", "WHERE status = #{1}", status).
    Add("ORDER BY id DESC")

items, total, err := dba.Page[User](q, page, size)
// Internally: Var(F, "COUNT(1)") for total, then Add("LIMIT ? OFFSET ?") for data
```

When `total == 0`, the data query is skipped entirely.

---

## Generic DAO

```go
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

id, _ := dao.Create(User{Name: "alice"})          // hook sets created_at
user, _ := dao.GetByID(id)                         // *User, nil when not found
affected, _ := dao.Update(data, "id = #{1}", id)  // map skips hooks
items, _ := dao.List("age > #{1}", 18)            // []User
count, _ := dao.Count("age > #{1}", 18)           // int64
```

### Cross-DAO transactions

```go
q.Transaction(func(tx *dba.SQL) error {
    uid, err := userDao.WithTx(tx).Create(User{Name: "alice"})
    if err != nil { return err }
    _, err = orderDao.WithTx(tx).Create(Order{UserID: int(uid), Product: "widget"})
    return err // nil → commit, error → rollback
})
```

---

## Middleware

Onion model — all queries go through the chain. Attach logging, metrics, tracing.

```go
q = q.Use(dba.LogMiddleware(slog.Default(), 200*time.Millisecond))
```

Custom middleware:

```go
q = q.Use(func(next dba.ExecFunc) dba.ExecFunc {
    return func(ctx context.Context, query string, args []any) (any, error) {
        start := time.Now()
        result, err := next(ctx, query, args)
        log.Printf("[%s] %s", time.Since(start), query)
        return result, err
    }
})
```

---

## Terminal Methods

- `Get(dest) (bool, error)` — single row, `(false, nil)` when not found
- `List(dest) error` — slice pointer
- `Exec() (sql.Result, error)` — INSERT/UPDATE/DELETE
- `Rows() (*sqlx.Rows, error)` — streaming large result sets
- `ToSQL() (string, []any, error)` — debug without execution

## Utilities

```go
count, found, _ := dba.Scalar[int64](q.Add("SELECT COUNT(1) FROM users"))

ok := dba.IsOk(value) // true for non-nil, non-empty string, non-empty slice/map
```
