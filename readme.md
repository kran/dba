# dba

An immutable, chainable SQL builder for `sqlx` — **SQL as the language, not as a DSL**.

No ORM, no code generation, no query-builder API to learn. You write SQL; dba
handles the plumbing: parameters, quoting, fragments, execution.

## Quick start

```go
q := dba.NewFromSqlx(db)

// SQL is the template — macros add plumbing, never replace the language
q.Add("SELECT * FROM users WHERE status = #{1} AND age >= #{2}",
    "active", 18)

// Conditional clauses
q.AddIf(minAge > 0, "AND age >= #{1}", minAge)

// Reusable fragments with parameters attached
q.Var("where", " WHERE status = #{1}", "active")
q.Add("SELECT * FROM users${where} ${order:ORDER BY id}")

// Execute
user := User{}
found, err := q.Get(&user)
```

## Why not just sqlx?

```go
// sqlx — string wrangling, manual arg spread, sqlx.In in a separate call
query := "SELECT * FROM users WHERE status = ?"
args := []any{"active"}
if name != "" {
    query += " AND name = ?"
    args = append(args, name)
}
query, args, _ = sqlx.In("SELECT * FROM users WHERE id IN (?)", ids)

// dba — the SQL reads as SQL
q.Add("SELECT * FROM users WHERE status = #{1}", "active").
    AddIf(name != "", "AND name = #{1}", name).
    Add("AND id IN (#{1|expand})", ids)
```

| Pain point | Raw sqlx | dba |
|---|---|---|
| `IN (?)` expansion | `sqlx.In()` in a separate call | `#{1\|expand}` explicit expansion |
| Conditional clauses | String concat + manual args | `AddIf(cond, ...)` |
| Reusable fragments | Copy-paste SQL | `Var(key, fragment, args...)` |
| Count + data pagination | Two maintained SQL strings | `Page[T](q, page, size)` |
| Identifier quoting | Manual per dialect | `@{table}` / `#{1\|quote}` / quoter |
| Tracing/logging | Wrap every call | `Use(LogHook(...))` — once |

## Core ideas

**SQL is the language.** SQL (recursive CTEs, window functions, set operations)
is a complete language — any DSL that tries to cover it either surrenders to
`Raw()` on complex queries or balloons into its own language (GORM's API
surface). dba doesn't abstract SQL; it adds plumbing *around* it. The template
**is** the final SQL — `ToSQL()` shows exactly what runs.

**Three mechanisms, each with one job:**

| Mechanism | Layer | Job |
|---|---|---|
| `#{...}` `!{...}` `@{...}` | rendering | values, identifiers, raw text |
| `\|pipe` | rendering | value rendering control (the only semantic layer) |
| `${...}` | structure | template fragments (reuse, override, defer decisions) |

## Syntax reference

### Macros

| Syntax | What it does | Example |
|---|---|---|
| `#{1}` | Positional parameter, single value | `WHERE id = #{1}` → `WHERE id = $1` |
| `#{name}` | Named parameter from map/struct | `#{id}` + `map["id"]` |
| `#{1\|pipe}` | Pipe override — custom rendering | `IN (#{1\|expand})` → `$1, $2, $3` |
| `!{1}` | Raw injection of parameter 1 (caller is responsible for safety) | `to !{1}` → `to /tmp/out.csv` |
| `@{users}` | **Literal** identifier — content is the identifier name, quoted safely | `SELECT @{users}.name` → `` SELECT `users`.name `` |
| `${key:default}` | Fillable fragment with fallback | `${order:ORDER BY id}` |
| `XX{...}` | Double prefix escapes to a literal `X{` | `##{1}` → literal `#{1}` |

Macros are only recognized when the prefix **immediately** precedes `{`
(`#{1}` yes, `# {1}` no) — so MySQL `# comments`, PG `$1`, `@x` variables,
`#temp` tables and `!=` are all untouched. Quoted strings (`'...'`, `"..."`,
`` `...` ``) are skipped entirely — JSON literals and `LIKE '%{x}%'` are safe.

### Pipes — the rendering layer

Pipes receive the macro content as a **literal string** and decide what to do
with it: use it directly, or resolve it as a parameter key via `ctx.Resolve`.

| Pipe | Kind | Behavior |
|---|---|---|
| `bind` | parameter | one value → placeholder + arg (`#{}` default) |
| `expand` | parameter | slice → separate placeholders (`IN (#{1\|expand})`) |
| `raw` | parameter | value → raw text (`to !{1}`) |
| `quote` | parameter | value → quoted identifier (`SELECT #{1\|quote} FROM t`, `"name"`) |
| `literalquote` | literal | content is the identifier name → quoted (`@{users}`) |

`#` is a plain macro whose default pipe is `bind` — no privileges. Any macro
content can carry its own pipe: `#{1|quote}`, `@{x|raw}`.

### Variables — the structure layer

```go
// Fillable slots with inline defaults. Spacing conventions:
//   • a variable sits flush against the preceding text
//   • spacing between variables is written before the later one
//   • Var content conventionally starts with a space
//   • inline default text is trimmed — keep spacing in the template
q.Add("SELECT ${F:*} FROM users${where:} ${order:ORDER BY id}")
// → SELECT * FROM users ORDER BY id          (defaults apply)
// → SELECT id, name FROM users WHERE status = $1 ORDER BY id DESC
//   (after Var("where", ...) + Var(F, "id, name") + Var("order", ...))

// Parameters are attached to the fragment, not the call site
q.Var("where", " WHERE status = #{1} AND age >= #{2}", "active", 18)
q.Add("SELECT * FROM users${where}")   // caller doesn't know the args

// Immutable — each Var returns a new copy; the base is untouched
base  := q.Add("SELECT ${F:*} FROM users${where:} ${order:ORDER BY id}")
count := base.Var(dba.F, "COUNT(1)")          // independent query
data  := base.Var("where", " WHERE id > #{1}", 100)
```

Variables are the *structure* layer: their content is a template (macros
included) and renders recursively. They are deliberately invisible to pipes —
the dependency is one-way (structure → rendering), so a pipe can never reach
back into the structure layer.

## Building

```go
q.Add("SELECT * FROM users WHERE id = #{1}", 42)           // positional
q.Add("SELECT * FROM users WHERE id = #{id}", map{"id": 42}) // named (map/struct)
q.AddIf(name != "", "AND name = #{1}", name)               // conditional
q.Var("where", "WHERE x = #{1}", 1)                        // fragment
q.Batch(rows)                                              // parenthesized value groups
```

`Unsafe()` disables macro parsing for raw dialect SQL you don't want touched.

## DML helpers

Columns are sorted lexicographically for stable SQL; `omitempty` drops zero
values (including `sql.Null*` with `Valid=false`).

```go
type User struct {
    ID        int    `db:"id,omitempty"`
    Name      string `db:"name"`
    CreatedAt string `db:"created_at,omitempty"`
}

q.Insert("users", User{Name: "alice"})
// INSERT INTO "users" ("name") VALUES ($1)     — ID and CreatedAt omitted

q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 42)
// UPDATE "users" SET "name"=$1 WHERE id = $2

q.Delete("users", "id = #{1}", 42)

q.BatchInsert("users", anySlice(users))        // one statement, N rows
// INSERT INTO "users" ("name") VALUES ($1), ($2), ($3)

q.Update("stats", map[string]any{
    "views": dba.Expr("views + 1"),            // raw SQL expressions in values
    "score": dba.Expr("score + #{1}", 10),
}, "id = #{1}", 1)
```

## Executing

```go
found, err := q.Get(&user)             // single row; (false, nil) when not found
err := q.List(&users)                  // slice pointer; also *[]map[string]any
res, err := q.Exec()                   // INSERT/UPDATE/DELETE
rows, err := q.Rows()                  // streaming large result sets
sql, args, err := q.ToSQL()            // inspect without executing
```

## Dialects

Two pure functions, nothing else. dba never rewrites SQL syntax — dialect
differences (`LIMIT ? OFFSET ?`, JSON operators, casts) live in your template,
as SQL should. Quoting and placeholders are pluggable:

```go
q := dba.NewFromSqlx(db).
    Quoter(dba.MySQLQuoter)   // `name`  — default is MySQL-style
    // Quoter(dba.AnsiQuoter) // "name"  — PG/SQLite/SQL Server
    // Formater(dba.DollarFormat) // $1, $2 — PG
```

No dialect matrix, no database-version coupling — a new database costs two
functions, and you are never forced to upgrade a database to use the library.

## Generic DAO

```go
type User struct {
    ID        int       `db:"id,omitempty"`
    Name      string    `db:"name"`
    CreatedAt time.Time `db:"created_at"`
}
func (u *User) BeforeCreate() error {   // hooks for timestamps, defaults
    u.CreatedAt = time.Now()
    return nil
}

dao := dba.NewDao[User](q, "users")

id, _ := dao.Create(User{Name: "alice"})      // hook runs; returns new ID
user, _ := dao.GetByID(id)                     // *User, nil when not found
items, _ := dao.List("age > #{1}", 18)         // []User
count, _ := dao.Count("age > #{1}", 18)
affected, _ := dao.Update(data, "id = #{1}", id)
deleted, _ := dao.Delete("id = #{1}", id)

// Join with the table's own quoting, via alias variables:
m := dao.Vars("u")   // {"u.as": "`users` AS `u`", "u": "`u`", "u.pk": "`u`.`id`"}
q = q.Vars(m)         // register the fragment map
q.Add("SELECT ${u.pk} FROM ${u.as} JOIN orders o ON o.user_id = ${u.pk}")

// Cross-DAO transactions
q.Transaction(func(tx *dba.SQL) error {
    uid, err := userDao.WithTx(tx).Create(User{Name: "alice"})
    if err != nil {
        return err
    }
    _, err = orderDao.WithTx(tx).Create(Order{UserID: int(uid)})
    return err // nil → commit, error → rollback
})
```

## Extending

**Pipes are the single semantic extension point.** Register once, use in any
template:

```go
q := db.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
    v, err := ctx.Resolve(content)              // parameter, or literal?
    if err != nil {
        return err
    }
    ctx.AddParam(strings.ToUpper(fmt.Sprint(v)))
    return nil
})
q.Add("WHERE name = #{1|upper}", "bob")

// Macros are syntax aliases for pipes — local dialect, shared semantics
q = q.RegisterMacro('^', "upper")
q.Add("WHERE name = ^{1}", "bob")               // same as #{1|upper}
```

The pipe receives the macro content **literally** and decides itself whether to
treat it as a parameter key (`ctx.Resolve`) or use it directly — so custom
pipes are free to do either. Dialect-specific needs (e.g. DuckDB's
`COPY ... TO` not accepting bound output paths) are solved with a 10-line
custom pipe at the project level — never by growing the library.

Both registrations are instance-scoped and copy-on-write — no global state, no
concurrency issues. `$` is the reserved structure-layer prefix.

## Middleware

Onion model — every query passes through the chain:

```go
q = q.Use(dba.LogHook(slog.Default(), 200*time.Millisecond, true))
// or
q = q.Use(func(next dba.ExecFunc) dba.ExecFunc {
    return func(ctx context.Context, query string, args []any) (any, error) {
        start := time.Now()
        result, err := next(ctx, query, args)
        log.Printf("[%s] %s", time.Since(start), query)
        return result, err
    }
})
```

## Utilities

```go
count, found, _ := dba.Scalar[int64](q.Add("SELECT COUNT(1) FROM users"))
items, total, _ := dba.Page[User](q, page, size)   // skips data query when total==0
ok := dba.IsOk(v)                    // non-nil, non-empty string/slice/map
dba.Map / dba.IndexBy / dba.GroupBy  // generic slice helpers
```

## Design notes

- **No DSL.** SQL is a complete language; abstracting it means either a
  surrender-to-`Raw()` split brain or an API surface that chases SQL's
  capabilities forever. The template *is* the SQL — the only abstraction is
  plumbing (parameters, quoting, fragments).
- **Pipes receive literals, not values.** The pipe decides whether content is
  a parameter key or a literal — value-source decisions live in the rendering
  layer, so custom pipes are free to do either.
- **Structure and rendering are separate layers.** `${}` fragments render
  recursively and may contain macros; pipes can never reach back into
  fragments (one-way dependency — no cycles, no cross-layer coupling).
- **Dialect cost is two functions.** Quoter + Formater, and nothing else
  parses or rewrites SQL. New databases, old versions — both are free.
- **Mechanisms over features.** Everything is a small, composable mechanism
  (macro, pipe, variable, hook, quoter). Features are assembled from
  mechanisms at the call site, not added to the library.

## Tests

```
go test ./...          # 191 tests
go test -race ./...    # race detector
```
