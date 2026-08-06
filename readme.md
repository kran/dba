# dba

An immutable, chainable SQL builder for `sqlx` — **SQL as the language, not as a DSL**.

No ORM, no code generation, no query-builder API to learn. You write SQL; dba
handles the plumbing: parameters, quoting, fragments, execution.

## Quick start

```go
db := dba.NewFromSqlx(pool) // base instance — never mutated

// One template, three uses. Fragments carry their own parameters —
// the call site never sees the args.
base := db.Add("SELECT ${F:*} FROM users ${where:} ${order:ORDER BY id}") // defaults: * / empty / ORDER BY id

// list: active adults, newest first — fill two slots
list := base.
    Var("where", "WHERE status = #{1} AND age >= #{2}", "active", 18).
    Var("order", "ORDER BY created_at DESC")
// → SELECT * FROM users WHERE status = $1 AND age >= $2 ORDER BY created_at DESC
var users []User
err := list.List(&users)

// count: same filter, swap F to COUNT — base and list stay untouched
total, found, err := dba.Scalar[int64](list.Var(dba.F, "COUNT(1)"))
// → SELECT COUNT(1) FROM users WHERE status = $1 AND age >= $2 ORDER BY created_at DESC
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

// dba — the SQL reads as SQL (chained: each call returns a new builder)
built := db.Add("SELECT * FROM users WHERE status = #{1}", "active").
    AddIf(name != "", "AND name = #{1}", name).
    Add("AND id IN (#{1|expand})", ids)
```

| Pain point | Raw sqlx | dba |
|---|---|---|
| `IN (?)` expansion | `sqlx.In()` in a separate call | `#{1\|expand}` explicit expansion |
| Conditional clauses | String concat + manual args | `AddIf(cond, ...)` |
| Reusable fragments | Copy-paste SQL | `Var(key, fragment, args...)` |
| Count + data pagination | Two maintained SQL strings | `Page[T](db, page, size)` |
| Identifier quoting | Manual per dialect | `@{table}` / `#{1\|quote}` / quoter |
| Tracing/logging | Wrap every call | `SetLogger(NewLogger(...))` — once |

## Core ideas

**SQL is the language.** SQL (recursive CTEs, window functions, set operations)
is a complete language — any DSL that tries to cover it either surrenders to
`Raw()` on complex queries or balloons into its own language (GORM's API
surface). dba doesn't abstract SQL; it adds plumbing *around* it. The template
**is** the final SQL — `ToSQL()` shows exactly what runs.

**Two mechanisms, each with one job:**

| Mechanism | Layer | Job |
|---|---|---|
| `#{...}` | rendering | values — the default pipe is `bind`; `\|pipe` overrides rendering (`!`/`@` are shortcuts for two pipes) |
| `${...}` | structure | template fragments (reuse, override, defer decisions) |

## Syntax reference

### Macros

| Syntax | What it does | Example |
|---|---|---|
| `#{1}` | Positional parameter, single value | `WHERE id = #{1}` → `WHERE id = $1` |
| `#{name}` | Named parameter — non-numeric content resolves from the last arg (map/struct, sqlx convention); map/struct field values may themselves be Node (inlined) | `#{id}` + `map["id"]` |
| `#{1\|pipe}` | Pipe override — custom rendering | `IN (#{1\|expand})` → `$1, $2, $3` |
| `${key:default}` | Fillable fragment with fallback | `${order:ORDER BY id}` |
| `XX{...}` | Double prefix escapes to a literal `X{` | `##{1}` → literal `#{1}` |

Shortcuts — `!` and `@` are convenience prefixes for two pipes:
`!{1}` ≡ `#{1\|raw}` (raw value injection, caller responsible for safety);
`@{users}` ≡ the `literalquote` pipe (content *is* the identifier name, quoted safely).

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
// Fillable slots with inline defaults. Write naturally — spaces live in the
// template, Var content and inline defaults need no leading space.
// An empty fragment leaves the template's spacing (double space) — harmless.
noFilter := db.Add("SELECT ${F:*} FROM users ${where:} ${order:ORDER BY id}")
// → SELECT * FROM users  ORDER BY id          (defaults apply — empty where leaves a gap)
// → SELECT id, name FROM users WHERE status = $1 AND age >= $2 ORDER BY id DESC
//   (after Var("where", ...) + Var(F, "id, name") + Var("order", ...))

// Parameters are attached to the fragment, not the call site
filtered = noFilter.Var("where", "WHERE status = #{1} AND age >= #{2}", "active", 18)

// Immutable — each Var returns a new copy; the base is untouched
base  := db.Add("SELECT ${F:*} FROM users ${where:} ${order:ORDER BY id}")
count := base.Var(dba.F, "COUNT(1)")          // independent query, dba.F is just 'F'
data  := base.Var("where", "WHERE id > #{1}", 100)
```

Variables are the *structure* layer: their content is a template (macros
included) and renders recursively. They are deliberately invisible to pipes —
the dependency is one-way (structure → rendering), so a pipe can never reach
back into the structure layer.

## Building

```go
// Every method returns a NEW builder — db is never mutated; reassign or chain.
db.Add("SELECT * FROM users WHERE id = #{1}", 42)           // positional
db.Add("SELECT * FROM users WHERE id = #{id}", map{"id": 42}) // named (map/struct)
db.AddIf(name != "", "AND name = #{1}", name)               // conditional
db.Var("where", "WHERE x = #{1}", 1)                        // fragment
db.Batch(rows)                                              // parenthesized value groups
```

`Unsafe()` switches to sqlx's unsafe mode (struct field-to-column mapping
mismatches are ignored instead of erroring) — for ad-hoc scans against
loosely-shaped rows. Macro parsing is unaffected.

## DML helpers

Columns are sorted lexicographically for stable SQL; `omitempty` drops zero
values (including `sql.Null*` with `Valid=false`). Pointer fields follow the
encoding/json convention: `nil` is skipped, `&0` is kept — the escape hatch
for writing a zero value through an omitempty column. A `sql.Null*` tagged
omitempty cannot express an explicit NULL (write via map instead).

```go
type User struct {
    ID        int    `db:"id,omitempty"`
    Name      string `db:"name"`
    CreatedAt string `db:"created_at,omitempty"`
}

// Every method returns a NEW builder — db is never mutated.
db.Insert("users", User{Name: "alice"})
// INSERT INTO "users" ("name") VALUES ($1)     — ID and CreatedAt omitted

db.Update("users", dba.H{"name": "bob"}, "id = #{1}", 42)
// UPDATE "users" SET "name"=$1 WHERE id = $2

db.Delete("users", "id = #{1}", 42)

db.BatchInsert("users", anySlice(users))        // one statement, N rows
// INSERT INTO "users" ("name") VALUES ($1), ($2), ($3)

db.Update("stats", dba.H{
    "views": dba.Expr("views + 1"),            // raw SQL expressions in values
    "score": dba.Expr("score + #{1}", 10),
}, "id = #{1}", 1)
```

## Two ways to carry SQL fragments

`${var}` and Node parameters both inline SQL text, but serve different jobs:

```
${var}    — the template's configurable slot: named, late-bound, overridable
            (${F:*}, ${order:ORDER BY id}) — structure layer
Node arg  — SQL text travelling as data: positional, immediate, travels
            with the value (a column value of NOW(), an IN subquery)
```

Both render through the same recursion (depth-limited, cycle-safe). The
generators already follow this split: `Insert`/`Update` accept Node values,
`Select`/`Page` use `${F:*}` slots.

## Executing

```go
found, err := db.Get(&user)             // single row; (false, nil) when not found
err := db.List(&users)                  // slice pointer (sqlx mapping)
ms, err := db.ListMap()                 // rows as []map[string]any — unknown columns
m, found, err := db.GetMap()            // single row as map[string]any
res, err := db.Exec()                   // INSERT/UPDATE/DELETE
rows, err := db.Rows()                  // streaming large result sets
sql, args, err := db.ToSQL()            // inspect without executing
```

## Dialects

Two pure functions, nothing else. dba never rewrites SQL syntax — dialect
differences (`LIMIT ? OFFSET ?`, JSON operators, casts) live in your template,
as SQL should. Quoting and placeholders are pluggable:

```go
db := dba.NewFromSqlx(pool).
    Quoter(dba.MySQLQuoter)   // `name`  — default is MySQL-style
    // Quoter(dba.AnsiQuoter) // "name"  — PG/SQLite/SQL Server
    // Formatter(dba.DollarFormat) // $1, $2 — PG
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

dao := dba.NewDao[User](db, "users")

id, _ := dao.Create(User{Name: "alice"})      // hook runs; returns new ID
user, _ := dao.GetByID(id)                     // *User, nil when not found
items, _ := dao.List("age > #{1}", 18)         // []User
count, _ := dao.Count("age > #{1}", 18)
affected, _ := dao.Update(data, "id = #{1}", id)
deleted, _ := dao.Delete("id = #{1}", id)

// Join with the table's own quoting, via alias variables:
m := dao.Vars("u")   // {"u.as": "`users` AS `u`", "u": "`u`", "u.pk": "`u`.`id`"}
joined := db.Vars(m).   // register the fragment map
    Add("SELECT ${u.pk} FROM ${u.as} JOIN orders o ON o.user_id = ${u.pk}")

// Cross-DAO transactions
db.Transaction(func(tx *dba.SQL) error {
    uid, err := userDao.WithTx(tx).Create(&User{Name: "alice"})
    if err != nil {
        return err
    }
    _, err = orderDao.WithTx(tx).Create(&Order{UserID: int(uid)})
    return err // nil → commit, error → rollback
})
```

## Extending

**Pipes are the single semantic extension point.** Register once, use in any
template:

```go
db := pool.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
    v, err := ctx.Resolve(content)              // parameter, or literal?
    if err != nil {
        return err
    }
    return ctx.Bind(strings.ToUpper(fmt.Sprint(v)))  // Bind: Node 内联, 普通值占位符
})
upper := db.Add("WHERE name = #{1|upper}", "bob")

// Macros are syntax aliases for pipes — local dialect, shared semantics
alias := db.RegisterMacro('^', "upper").
    Add("WHERE name = ^{1}", "bob")             // same as #{1|upper}
```

The pipe receives the macro content **literally** and decides itself whether to
treat it as a parameter key (`ctx.Resolve`) or use it directly — so custom
pipes are free to do either. Bind any resolved value through `ctx.Bind` —
it is the single place where Node parameters are inlined (and the only way
a custom pipe stays safe). Dialect-specific needs (e.g. DuckDB's
`COPY ... TO` not accepting bound output paths) are solved with a 10-line
custom pipe at the project level — never by growing the library.

Both registrations are instance-scoped and copy-on-write — no global state, no
concurrency issues. `$` is the reserved structure-layer prefix.

## Observing executions

A single callback is invoked after every execution — pass `begin` so the
observer computes duration or opens tracing spans itself:

```go
logged := db.SetLogger(dba.NewLogger(slog.Default(), 200*time.Millisecond, true))
// or a custom observer:
custom := db.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
    log.Printf("[%s] %s", time.Since(begin), query)
})
```

`SetLogger(nil)` silences. Observers never alter the execution flow.

## Utilities

```go
count, found, _ := dba.Scalar[int64](db.Add("SELECT COUNT(1) FROM users"))
items, total, _ := dba.Page[User](db, page, size)   // skips data query when total==0
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
- **Dialect cost is two functions.** Quoter + Formatter, and nothing else
  parses or rewrites SQL. New databases, old versions — both are free.
- **Mechanisms over features.** Everything is a small, composable mechanism
  (macro, pipe, variable, quoter, logger callback). Features are assembled from
  mechanisms at the call site, not added to the library.

## Tests

```
go test ./...          # 191 tests
go test -race ./...    # race detector
```
