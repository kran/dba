# stupidql

`stupidql` is an immutable, dynamic SQL builder and execution engine built on top of `sqlx`.

## Design Philosophy

The design of this library follows these core principles:

1.  **Immutability**: Core state-mutating methods (`Add`, `Mark`, `AddIf`) return new instance copies. The original instance's state remains unchanged, ensuring safety during concurrent operations and method chaining.
2.  **Explicit Declaration over Implicit Filtering**: When mapping structs to SQL statements, zero values (e.g., `0`, `""`, `false`) are retained by default. A field is only omitted from `INSERT` or `UPDATE` statements if its struct tag explicitly includes `omitempty` (e.g., `db:"fieldname,omitempty"`) and its value evaluates to the zero value.
3.  **Deterministic Generation**: When extracting columns and values from Maps or Structs to generate `INSERT` or `UPDATE` statements, keys are strictly sorted in lexicographical order. This guarantees that the generated SQL strings are absolutely consistent, optimizing the utilization of the database's Query Plan Cache.
4.  **Macro Engine**: Custom wrappers (`#{}`, `@{}`, `!{}`) handle parameter binding, identifier escaping, and raw string replacement. This abstracts away driver-specific placeholders (e.g., `?` vs. `$1`), deferring the final dialect translation and slice expansion to `sqlx`'s `Rebind` and `In` functions.
5.  **Underlying Delegation**: The library does not reinvent reflection mapping or database driver interactions. Data querying and struct conversions are entirely delegated to `sqlx` and its sub-package `reflectx`.

---

## Initialization

`stupidql` requires an initialized `*sqlx.DB` instance. It automatically configures the appropriate identifier quoter based on the driver name (e.g., `mysql` or `postgres`).

```go
dbx, _ := sqlx.Connect("postgres", "user=foo dbname=bar sslmode=disable")
q := stupidql.NewStupidQL(dbx)
```

---

## API Usage

The API architecture is divided into three functional layers:

### 1. Core Query Builder

Used for direct manipulation and appending of SQL fragments, supporting macro variable substitution. Macro data sources can be positional arguments or named properties extracted from the final argument (which must be a Map or Struct).

* **`#{key}` (Parameter Binding)**: Converted to a standard placeholder `?`. The variable is added to the argument list, preventing SQL injection.
* **`@{key}` (Identifier Escaping)**: Safely escapes table or column names (e.g., formats as `` `table` `` or `"table"` depending on the dialect).
* **`!{key}` (Raw Replacement)**: Injects the variable directly into the SQL string as plain text (poses an injection risk; must be sanitized by the caller).

**Methods:**
* `Add(query string, args ...any)`: Parses macros and appends the SQL fragment.
* `AddIf(cond bool, query string, args ...any)`: Appends the SQL fragment only if `cond` is `true`.
* `Mark(name string, query string, args ...any)`: Registers or replaces a named placeholder within the current query chain.

```go
// Positional parameter replacement
q1 := q.Add("SELECT * FROM users WHERE status = #{1}", "active")

// Named parameter replacement (using a Map)
q2 := q.Add("SELECT * FROM @{table} WHERE id = #{id}", map[string]any{
    "table": "users",
    "id":    42,
})

// Mark reservation and replacement
q3 := q.Add("SELECT").Mark("FIELD*", "id, name").Add("FROM users")
```

### 2. DML Abstraction Layer

Generates standard Data Manipulation Language (DML) statements by parsing struct tags via `reflectx`. Struct tags support the `db:"name,omitempty"` syntax.

* **`Insert(table string, data any)`**: Automatically generates an `INSERT INTO` statement based on the provided Struct or Map. Zero-value fields with the `omitempty` tag are excluded.
* **`Update(table string, data any, where string, args ...any)`**: Generates an `UPDATE table SET ...` statement. Zero-value fields with the `omitempty` tag are excluded.
* **`Select(table string, where string, args ...any)`**: Generates a basic `SELECT * FROM table WHERE ...` statement. It internally reserves `FIELD*` via `Mark` for subsequent modifications.
* **`Delete(table string, where string, args ...any)`**: Generates a `DELETE FROM table WHERE ...` statement.

```go
type User struct {
    ID     int    `db:"id,omitempty"` // Automatically omitted if 0
    Name   string `db:"name"`
    Status string `db:"status"`       // No omitempty; updated/inserted even if ""
}

// Generates: INSERT INTO "users" ("name", "status") VALUES (?, ?)
qInsert := q.Insert("users", User{Name: "Alice", Status: "active"})

// Generates: UPDATE "users" SET "name"=?, "status"=? WHERE id = ?
qUpdate := q.Update("users", User{Name: "Bob", Status: ""}, "WHERE id = #{1}", 42)
```

### 3. Execution & Mapping Layer

This layer handles terminal operations. Invoking these methods triggers the underlying `build()` process (which executes slice expansion and dialect-specific placeholder conversion) and submits the final SQL and arguments to `sqlx` for execution.

* **`List(dest interface{}) error`**: Executes the query and maps multiple rows into the provided Slice pointer. Corresponds to `sqlx.SelectContext`.
* **`Get(dest interface{}) error`**: Executes the query and maps a single row into the provided Struct pointer. Corresponds to `sqlx.GetContext`.
* **`Exec() (sql.Result, error)`**: Executes a non-query statement (e.g., INSERT/UPDATE/DELETE) and returns the result and error.
* **`Rows() (*sqlx.Rows, error)`**: Returns the raw cursor for streaming large result sets. Corresponds to `sqlx.QueryxContext`.

```go
var users []User
err := q.Add("SELECT * FROM users WHERE status = #{1}", "active").List(&users)

var u User
err = q.Add("SELECT * FROM users WHERE id = #{1}", 1).Get(&u)

result, err := q.Update("users", User{Name: "Charlie"}, "WHERE id = #{1}", 1).Exec()
```

---

## Transaction Management

Transactions are managed via closures to prevent connection leaks and eliminate manual rollback boilerplate.

* **`Transaction(fn func(*StupidQL) error) error`**: Initiates a transaction and passes a `StupidQL` instance bound to the transaction object into the closure. If the closure returns an `error` or a `panic` occurs, the transaction is automatically rolled back; if successful, the transaction is committed.

```go
err := q.Transaction(func(tx *stupidql.StupidQL) error {
    _, err := tx.Insert("users", User{Name: "Dave"}).Exec()
    if err != nil {
        return err // Triggers Rollback
    }

    _, err = tx.Update("stats", map[string]any{"count": 1}, "WHERE id = 1").Exec()
    if err != nil {
        return err // Triggers Rollback
    }
    
    return nil // Triggers Commit
})
```