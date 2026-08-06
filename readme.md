# dba

A SQL-respecting query builder for Go.

dba does not translate SQL into method calls, nor hide it behind an object
model — you write the SQL, dba handles the rest of the plumbing: dynamic
condition assembly, parameter numbering and dialect placeholders, fragment
reuse, struct/map to INSERT/UPDATE mapping, and high-frequency collaboration
patterns like pagination. It sits in the gap between sqlx (scan enhancement)
and squirrel (AST-style builder), philosophically close to MyBatis dynamic
SQL, but with an immutable chainable API instead of XML.

```go
db, _ := dba.Open("pgx", dsn)

var users []User
err := db.Select("users", "status = #{1}", "active").
    AddIf(name != "", "AND name LIKE #{1}", "%"+name+"%").
    AddIf(len(ids) > 0, "AND id IN (#{1|expand})", ids).
    Add("ORDER BY created_at DESC").
    List(&users)
```

One sentence for the core property: **the builder is immutable**. Every method
returns a new instance, so half-built queries can be safely passed across
functions, cached, and forked — this is the foundation of pagination, count
reuse, and similar patterns.

## Install

```
go get github.com/kran/dba
```

Depends on `jmoiron/sqlx`. Create via `Open(driver, dsn)` or
`NewFromSqlx(*sqlx.DB)`; placeholder format is chosen automatically by driver
(`$n` for the Postgres family, `?` otherwise) and identifier quoting too
(MySQL backticks, ANSI double quotes elsewhere), overridable with
`Formatter`/`Quoter`.

## Template language

dba's template has two core symbols, one per layer:

| Syntax | Layer | Meaning |
|---|---|---|
| `#{key}` | value | resolve an argument and bind it as a placeholder |
| `#{key\|pipe}` | value | resolve an argument, hand it to the named pipe |
| `${var}` | structure | expand a named variable (template recursion) |
| `${var:default}` | structure | expand default text when the variable is undefined |

### Value layer: `#{}` and pipes

Argument keys come in two forms. **Positional**: `#{1}`, `#{2}` address the
n-th argument of the current fragment — numbering is per-fragment, each `Add`
counts from 1, and fragments never interfere, so fragments can be freely
composed without renumbering. **Named**: `#{name}` resolves from the last
argument (struct or map) of the fragment; structs map by `db` tag, sharing the
same rules as row scanning.

```go
db.Add("WHERE age > #{1} AND city = #{2}", 18, "SH")       // positional
db.Add("WHERE name = #{Name} AND age > #{Age}", user)       // named: struct
db.Add("WHERE name = #{name}", dba.H{"name": "bob"})        // named: map (H is a map[string]any alias)
db.Add("WHERE a = #{1} AND b = #{key}", 1, dba.H{"key": 2}) // mixed: named source is the last arg
```

Pipes decide how an argument value enters the SQL. Five built-in:

```go
// bind (default): bind as a placeholder
db.Add("WHERE id = #{1}", 42)                        // → WHERE id = $1

// expand: expand a slice into comma-separated placeholders
db.Add("WHERE id IN (#{1|expand})", []int{1, 2, 3})  // → IN ($1, $2, $3)

// raw: inject the argument value as SQL text (caller attests safety, see "Safety boundaries")
db.Add("WHERE created > #{1|raw}", "NOW()")           // → WHERE created > NOW()

// quote: treat the argument value as an identifier, quoted per dialect (dynamic columns/sorting)
db.Add("ORDER BY #{1|quote}", sortCol)                // → ORDER BY "sort_col"

// literalquote: the macro content itself is the identifier (consumes no argument)
db.Add("SELECT * FROM #{users|literalquote}")         // → SELECT * FROM "users"
```

Two shorthand macros: `!{1}` ≡ `#{1|raw}`, `@{users}` ≡
`#{users|literalquote}`. An empty slice through expand yields `IN ()`, which
the database rejects with a syntax error — dba does not intercept; pair with
`AddIf(len(ids) > 0, ...)` for dynamic conditions.

### Structure layer: `${}` variables

Variables are named SQL fragments (template + their own arguments), expanded
recursively at render time, with isolated argument scopes:

```go
q := db.Add("SELECT * FROM t WHERE ${cond} ${order:ORDER BY id}").
    Var("cond", "status = #{1} AND ${scope}", "active").
    Var("scope", "org_id = #{1}", orgID)
// ${order:...} falls back to the default ORDER BY id when undefined
```

Variables are **late-bound**: referencing with `Add` before defining with
`Var` is legal; lookup happens at build time. This is the basis of the slot
protocol (see F/I/O below). Rendering recursion has a depth limit (64);
self-referential cycles produce a clear error instead of a stack overflow.

## Node: arguments as subtrees

`Expr(sql, args...)` returns a `Node` — a SQL fragment together with its own
arguments. **Wherever a Node appears in an argument position it is inlined
instead of bound as a placeholder**; this is the library-wide invariant:

```go
// Add argument
db.Add("WHERE updated < #{1}", dba.Expr("NOW() - INTERVAL #{1} DAY", 7))
// → WHERE updated < NOW() - INTERVAL $1 DAY

// Insert/Update field values
db.Update("counters", dba.H{
    "views": dba.Expr("views + #{1}", 1),  // inlined: views = views + $1
    "name":  "n",                           // plain value: placeholder
}, "id = #{1}", 5)

// expand slice elements (subqueries mixed into an IN list)
keys := []any{"alice", dba.Expr("lower(#{1})", input), "bob"}
db.Add("WHERE username IN (#{1|expand})", keys)
// → IN ($1, lower($2), $3)

// struct fields (a Node-typed field collapses to a single column)
type Event struct {
    Name    string   `db:"name"`
    Created dba.Node `db:"created"`
}
db.Insert("events", Event{Name: "e", Created: dba.Expr("NOW()")})
```

`*Node` is supported as well; a nil pointer binds as SQL NULL.

## CRUD generators

```go
db.Select("users", "age > #{1}", 18)          // SELECT ${F:*} FROM "users" WHERE age > $1
db.Insert("users", user)                       // struct or map
db.Update("users", changes, "id = #{1}", id)   // changes is a struct or map
db.Delete("users", "id = #{1}", id)
db.BatchInsert("users", entities)              // bulk, all columns (see omitempty)
db.Add("INSERT INTO t (a, b) VALUES").Batch(rows) // manual bulk value groups
```

Generators are just wrappers around `Add`; the resulting builder chains on:

```go
db.Insert("users", u).Add("ON CONFLICT (email) DO NOTHING").Exec()
```

### Struct mapping and omitempty

Column naming shares one mapper with scanning and named arguments: `db` tag
wins (used verbatim), no tag falls back to the lowercased field name,
`db:"-"` excludes. Nested/anonymous structs expand recursively into flat
columns; `driver.Valuer` implementers, `time.Time` and its aliases, and `Node`
collapse to atomic single columns.

Zero-value semantics on write are **per-field and visible at the declaration
site** (deliberately unlike GORM's implicit global zero-value skipping):

```
value field, no tag       → always written (zero values too)
value field + omitempty   → zero values skipped (auto-increment id / DB-defaulted columns)
pointer field + omitempty → nil skipped; non-nil kept (even pointing at zero) — the escape hatch for writing zeros
Node field                → single-column inline; zero Node + omitempty skipped
map                       → fully manual: write exactly what you pass
```

Pointer escape hatch example: `Views *int` tagged omitempty — nil means
"unset, skip this column", `&zero` means "I explicitly want 0".
`sql.NullString{Valid: false}` is the zero value, so with omitempty it is
skipped — for an explicit NULL use a pointer or a map.

`BatchInsert` forces all columns (ignores omitempty): a batch requires every
row to share the same column set — per-row omission would drift the set.

## Slot protocol: F / I / O

Generators and utilities cooperate through three conventional variable names,
all built on `${var:default}` late binding:

**F — column list slot.** `Select` generates `${F:*}`, `Page` forks the same
builder into a count query via `Var(F, "COUNT(1)")`. To feed hand-written SQL
to `Page`, embed `${F:*}` on the main chain:

```go
q := db.Add(`SELECT ${F:*} FROM orders o JOIN users u ON o.uid = u.id WHERE o.status = #{1}`, st)
items, total, err := dba.Page[Order](q, 1, 20)
```

`Page`'s count is a plain substitution — **not for GROUP BY / DISTINCT
queries**; write your own count for those.

**O — sort slot** (optional optimization). Write ORDER BY as
`${order:ORDER BY id DESC}` and `Page`'s count query clears it, saving a
pointless sort. A bare ORDER BY still works, just pays the sort on the count.

**I — INSERT modifier slot.** `Insert` generates `INSERT ${I:} INTO ...`,
empty by default. Chained `Add` can only append to the tail (RETURNING /
ON CONFLICT reachable), leaving INSERT-to-INTO a dead corner — this slot is
the only vent:

```go
db.Var(dba.I, "IGNORE").Insert("users", u)   // INSERT IGNORE INTO ...
```

## Execution and scanning

```go
err  := q.List(&users)              // many rows → slice pointer (struct/basic types)
ms, err := q.ListMap()              // many rows → []map[string]any (columns unknown at compile time)
found, err := q.Get(&user)          // one row; no match → found=false, no ErrNoRows
m, found, err := q.GetMap()         // one row → map
v, found, err := dba.Scalar[int64](q) // single value
result, err := q.Exec()             // non-query
rows, err := q.Rows()               // raw cursor (streaming)
sql, args, err := q.ToSQL()         // build only, don't execute
```

Builder errors (invalid generator input, mismatched Batch widths, ...)
accumulate along the chain; subsequent operations no-op and the error is
returned at execution or `ToSQL` time. You can also check `q.Error()` at any
point.

### Transactions

```go
err := db.Transaction(func(tx *dba.SQL) error {
    if _, err := tx.Insert("orders", order).Exec(); err != nil {
        return err
    }
    _, err := tx.Update("stock", dba.H{"n": dba.Expr("n - #{1}", 1)}, "sku = #{1}", sku).Exec()
    return err // returning an error (or panicking) rolls back
})
```

Inside an existing transaction, `Transaction` runs the function body directly
— no nesting. Manual `Begin`/`Commit`/`Rollback` also available.

### Logging

```go
db = db.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
    slog.Info("sql", "cost", time.Since(begin), "query", query, "err", err)
})
```

The callback fires after every execution; it never alters the execution flow.

## Dao: generic single-table helper

`Dao[T]` collects "the usual operations for one table" in one place; table
name and primary key are maintained at a single point:

```go
type User struct {
    ID      int64  `db:"id,omitempty"`
    Email   string `db:"email"`
    Name    string `db:"name"`
    Deleted int    `db:"deleted"`
}

// Optional hook: implementing it enables it
func (u *User) BeforeCreate() error {
    if u.Email == "" { return errors.New("email required") }
    return nil
}

userDao := dba.NewDao[User](db, "users")          // default pk id, override with .PK("uid")

id, err := userDao.Create(&u)                      // returns auto-increment/RETURNING pk
u, err  := userDao.GetByID(42)                     // not found → (nil, nil); check nil first
u, err  := userDao.Get("email = #{1}", email)
list, err := userDao.List("deleted = 0")
n, err  := userDao.Update(dba.H{"name": "x"}, "id = #{1}", 42)
n, err  := userDao.Delete("id = #{1}", 42)
ok, err := userDao.Exists("email = #{1}", email)
items, total, err := userDao.Page(1, 20, "deleted = 0")
n, err  := userDao.Batch(users)                    // bulk insert, returns affected rows
```

Design points:

**Not found is not an error.** `Get`/`GetByID` return `(nil, nil)` when
nothing matches — not `sql.ErrNoRows`. "No row" is a normal business outcome;
callers check nil, no `errors.Is` everywhere.

**Raw series keeps full chaining.** `RawCreate`/`RawSelect`/`RawBatch` return
a builder instead of executing, so complex needs (ON CONFLICT, RETURNING,
extra conditions) keep building on top of the Dao:

```go
err := userDao.RawCreate(&u).
    Add("ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name").
    Add("RETURNING " + "id").
    Get(&u.ID)
```

**Transaction propagation.** `dao.WithTx(tx)` returns a Dao copy bound to the
transaction; the original Dao is untouched:

```go
db.Transaction(func(tx *dba.SQL) error {
    d := userDao.WithTx(tx)
    // all operations on d go through the transaction
    return nil
})
```

**Alias variables for cross-table JOINs.** `dao.Vars(alias)` produces three
reference variables for the table declaration/alias/primary key — table name
and pk changes touch only the Dao:

```go
q := db.Add(`SELECT ${u}.*, ${o}.total
             FROM ${u.as} JOIN ${o.as} ON ${o}.uid = ${u.pk}
             WHERE ${u}.status = #{1}`, "active").
    Vars(userDao.Vars("u")).
    Vars(orderDao.Vars("o"))
// ${u.as} → "users" AS "u"    ${u} → "u"    ${u.pk} → "u"."id"
```

Column references are out of Vars' scope (`${u}.email` written bare) — the
column set is not part of the Dao's maintenance duty.

## Safety boundaries

Treat this section as a prerequisite, not a footnote.

**Arguments through `#{}` are safe** — every bind/expand path ends in a
driver-level placeholder, no concatenation. Risk concentrates in three
explicit "text injection" entry points; they exist for flexibility, and the
safety responsibility is the caller's.

First, **templates themselves must never concatenate untrusted input**.
`Add("WHERE name = '" + userInput + "'")` is injection in any library; in dba
the problem starts earlier — `#{`/`${` inside userInput would be parsed as
macros. Rule: template strings are literals in code or from trusted sources;
user data always goes through the argument position.

Second, **the raw pipe (`!{}`) is the only arbitrary-text injection point**.
Use it only for code-controlled SQL expressions (`NOW()`, `DEFAULT`), never
user input. Auditing is a global search for `|raw` and `!{` — that enumerates
the whole injection surface.

Third, **quote/literalquote escape per dialect but do not whitelist**.
`#{1|quote}` prevents identifier escaping, yet the user can still name any
column (an information-leak surface). For dynamic sort columns, pass through a
whitelist before the pipe.

## Extending

Custom pipes register via `RegisterPipe` (instance-scoped, copy-on-write, no
global state):

```go
db = db.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
    v, err := ctx.Resolve(content)   // resolve the argument
    if err != nil { return err }
    return ctx.Bind(strings.ToUpper(fmt.Sprint(v))) // Bind: Node inlines, plain values placeholder
})
db.Add("WHERE name = #{1|upper}", "bob")   // binds "BOB"
```

Inside a pipe, always route argument values through `ctx.Bind` — the Node
inline semantics are inherited automatically.

## Non-goals

dba deliberately does not do the following; read this section before filing a
feature request in these directions:

No relations and preloading — you write the JOIN; dba offers `Vars(alias)` to
maintain the references, nothing more. No database migrations. No
cross-dialect SQL translation — the dialect SQL you write goes out verbatim;
dba handles only two dialect differences: placeholders and identifier
quoting. No query caching. No interception of empty `IN ()` — that is the
database's syntax error; dba does not guess your intent. No nested
transactions / savepoints. No model validation — the `BeforeCreate`/
`BeforeUpdate` hooks are the seam left for you to do it.

## Appendix: macro prefix registration (compatibility)

`RegisterMacro(prefix, pipe)` registers a custom macro prefix (e.g. `^{1}` ≡
`#{1|upper}`). **Kept for existing projects; new code should use
`#{key|pipe}` directly — fully equivalent and no table lookup for readers.**
`#` and `$` are reserved prefixes; the built-in `@`/`!` are implemented
through this mechanism too.

```go
db = db.RegisterMacro('^', "upper")   // ^{1} ≡ #{1|upper}
```
