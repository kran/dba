package dba

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/reflectx"
)

// F is the default field variable name used by Select and Page.
const (
	F = "F"
	I = "I"
)

// H is a shorthand for map[string]any.
type H = map[string]any

var mapper = reflectx.NewMapperFunc("db", strings.ToLower)

// SQLExpr wraps a raw SQL expression for Insert/Update values.
type SQLExpr struct {
	Sql  string
	Args []any
}

// Expr creates a SQLExpr with optional bind arguments.
func Expr(sql string, args ...any) SQLExpr {
	return SQLExpr{Sql: sql, Args: args}
}

// Hook wraps an ExecFunc in onion-style middleware.
type Hook func(next ExecFunc) ExecFunc

// ExecFunc is the execution function passed through the middleware chain.
type ExecFunc func(ctx context.Context, query string, args []any) (any, error)

// PlaceholderFormat generates a placeholder for the n-th parameter.
type PlaceholderFormat func(idx int) string

// QmarkFormat returns "?" for every index.
func QmarkFormat(_ int) string { return "?" }

// DollarFormat returns "$1", "$2", ...
func DollarFormat(idx int) string { return "$" + strconv.Itoa(idx) }

// Quoter quotes an identifier for the target dialect.
type Quoter func(string) string

// MySQLQuoter wraps identifiers in backticks.
func MySQLQuoter(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// AnsiQuoter wraps identifiers in double quotes.
func AnsiQuoter(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

type node struct {
	rawSQL string
	args   []any
}

// SQL is an immutable, chainable query builder backed by sqlx.
type SQL struct {
	mainNodes []node
	varNodes  map[string]node

	db         sqlx.ExtContext
	rawDB      *sqlx.DB
	ctx        context.Context
	err        error
	quoter     Quoter
	format     PlaceholderFormat
	driverName string
	hooks      []Hook
	copyId     int
}

// NewFromSqlx creates a SQL builder from a sqlx.DB. Auto-detects driver for
// placeholder format and identifier quoting.
func NewFromSqlx(db *sqlx.DB) *SQL {
	driver := db.DriverName()
	quoter := AnsiQuoter
	format := QmarkFormat

	if driver == "postgres" || driver == "pgx" || driver == "pq" {
		quoter = AnsiQuoter
		format = DollarFormat
	} else if driver == "mysql" {
		quoter = MySQLQuoter
		format = QmarkFormat
	}

	return &SQL{
		mainNodes:  make([]node, 0),
		varNodes:   make(map[string]node),
		db:         db,
		rawDB:      db,
		ctx:        context.Background(),
		quoter:     quoter,
		format:     format,
		driverName: driver,
		copyId:     0,
	}
}

// Open connects to a database and returns a SQL builder. It is a
// convenience over sqlx.Connect + NewFromSqlx.
func Open(driver, dsn string) (*SQL, error) {
	db, err := sqlx.Connect(driver, dsn)
	if err != nil {
		return nil, err
	}
	return NewFromSqlx(db), nil
}

func (d *SQL) DB() *sqlx.DB { return d.rawDB }

func (d *SQL) Close() error {
	if d.rawDB != nil {
		return d.rawDB.Close()
	}
	return nil
}

func (d *SQL) copy() *SQL {
	clone := &SQL{
		mainNodes:  make([]node, len(d.mainNodes)),
		varNodes:   make(map[string]node),
		db:         d.db,
		rawDB:      d.rawDB,
		ctx:        d.ctx,
		err:        d.err,
		quoter:     d.quoter,
		format:     d.format,
		driverName: d.driverName,
		copyId:     d.copyId + 1,
	}
	copy(clone.mainNodes, d.mainNodes)
	for k, v := range d.varNodes {
		clone.varNodes[k] = v
	}
	if len(d.hooks) > 0 {
		clone.hooks = make([]Hook, len(d.hooks))
		copy(clone.hooks, d.hooks)
	}
	return clone
}

// WithContext returns a new builder with the given context.
func (d *SQL) WithContext(ctx context.Context) *SQL {
	clone := d.copy()
	clone.ctx = ctx
	return clone
}

// Quoter returns a new builder with the given identifier quoter.
func (d *SQL) Quoter(quoter Quoter) *SQL {
	clone := d.copy()
	clone.quoter = quoter
	return clone
}

// Format returns a new builder with the given placeholder format.
func (d *SQL) Format(formatter PlaceholderFormat) *SQL {
	clone := d.copy()
	clone.format = formatter
	return clone
}

// Unsafe returns a new builder that ignores unmapped columns.
func (d *SQL) Unsafe() *SQL {
	clone := d.copy()
	switch v := d.db.(type) {
	case *sqlx.DB:
		clone.db = v.Unsafe()
		if d.rawDB != nil {
			clone.rawDB = d.rawDB.Unsafe()
		}
	case *sqlx.Tx:
		clone.db = v.Unsafe()
	}
	return clone
}

// Use returns a new builder with the given hooks appended.
func (d *SQL) Use(mw ...Hook) *SQL {
	clone := d.copy()
	clone.hooks = append(clone.hooks, mw...)
	return clone
}

func (d *SQL) execute(fn ExecFunc) (any, error) {
	query, args, err := d.build()
	if err != nil {
		return nil, err
	}
	exec := fn
	for i := len(d.hooks) - 1; i >= 0; i-- {
		exec = d.hooks[i](exec)
	}
	return exec(d.ctx, query, args)
}

// Add appends a SQL fragment and returns a new builder.
func (d *SQL) Add(query string, args ...any) *SQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.mainNodes = append(clone.mainNodes, node{rawSQL: query, args: args})
	return clone
}

// AddIf conditionally appends a SQL fragment.
func (d *SQL) AddIf(cond bool, query string, args ...any) *SQL {
	if cond {
		return d.Add(query, args...)
	}
	return d
}

// Var registers a named variable for ${key} expansion.
func (d *SQL) Var(key string, query string, args ...any) *SQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.varNodes[key] = node{rawSQL: query, args: args}
	return clone
}

func (d *SQL) build() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}

	var sqlBuilder strings.Builder
	var finalArgs []any
	argCount := 0

	sqlBuilder.Grow(512)

	writeText := func(s string) {
		sqlBuilder.WriteString(s)
	}

	var parse func(n node) error
	parse = func(n node) error {
		str := n.rawSQL
		i := 0
		for i < len(str) {
			start := strings.IndexByte(str[i:], '{')
			if start == -1 {
				writeText(str[i:])
				break
			}
			start += i

			// doubled-prefix escape: ##{ → literal #{, etc.
			if start >= 2 && str[start-1] == str[start-2] && (str[start-1] == '#' || str[start-1] == '$' || str[start-1] == '@' || str[start-1] == '!') {
				writeText(str[i : start-2])
				sqlBuilder.WriteByte(str[start-1])
				sqlBuilder.WriteByte('{')
				i = start + 1
				continue
			}

			// check valid prefix
			if start == 0 || (str[start-1] != '#' && str[start-1] != '$' && str[start-1] != '@' && str[start-1] != '!') {
				writeText(str[i : start+1])
				i = start + 1
				continue
			}

			prefix := str[start-1]
			writeText(str[i : start-1])

			end := strings.IndexByte(str[start:], '}')
			if end == -1 {
				return fmt.Errorf("dba: unclosed brace in %q", str)
			}
			end += start
			content := str[start+1 : end]

			switch prefix {
			case '#':
				argVal, err := resolveArg(n.args, content)
				if err != nil {
					return err
				}

				rv := reflect.ValueOf(argVal)
				for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
					if rv.IsNil() {
						break
					}
					rv = rv.Elem()
				}

				if argVal != nil && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
					if rv.Len() == 0 {
						return fmt.Errorf("dba: empty slice passed to parameter #{%s}", content)
					}
					for j := 0; j < rv.Len(); j++ {
						if j > 0 {
							sqlBuilder.WriteString(", ")
						}
						argCount++
						sqlBuilder.WriteString(d.format(argCount))
						finalArgs = append(finalArgs, rv.Index(j).Interface())
					}
				} else {
					argCount++
					sqlBuilder.WriteString(d.format(argCount))
					finalArgs = append(finalArgs, argVal)
				}

			case '$':
				parts := strings.SplitN(content, ":", 2)
				key := strings.TrimSpace(parts[0])
				if varNode, ok := d.varNodes[key]; ok {
					if err := parse(varNode); err != nil {
						return err
					}
				} else if len(parts) == 2 {
					if err := parse(node{rawSQL: strings.TrimSpace(parts[1])}); err != nil {
						return err
					}
				}

			case '@':
				val, err := resolveArg(n.args, content)
				if err != nil {
					return err
				} else if val == nil {
					return fmt.Errorf("dba: @{%s} resolved to nil", content)
				}
				sqlBuilder.WriteString(d.quoter(fmt.Sprintf("%v", val)))

			case '!':
				val, err := resolveArg(n.args, content)
				if err != nil {
					return err
				} else if val == nil {
					return fmt.Errorf("dba: !{%s} resolved to nil", content)
				}
				sqlBuilder.WriteString(fmt.Sprintf("%v", val))
			}

			i = end + 1
		}
		return nil
	}

	for i, node := range d.mainNodes {
		if i > 0 {
			sqlBuilder.WriteString("\n")
		}
		if err := parse(node); err != nil {
			return "", nil, err
		}
	}

	return sqlBuilder.String(), finalArgs, nil
}

func resolveArg(args []any, content string) (any, error) {
	if idx, err := strconv.Atoi(content); err == nil {
		if idx < 1 || idx > len(args) {
			return nil, fmt.Errorf("dba: index %d out of bounds", idx)
		}
		return args[idx-1], nil
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("dba: no args")
	}
	return extractNamedArg(args[len(args)-1], content)
}

func extractNamedArg(src any, name string) (any, error) {
	rv := reflect.ValueOf(src)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, fmt.Errorf("dba: named args source is nil pointer")
		}
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("dba: map key must be string")
		}
		val := rv.MapIndex(reflect.ValueOf(name))
		if !val.IsValid() {
			return nil, fmt.Errorf("dba: named arg '%s' not found in map", name)
		}
		return val.Interface(), nil
	}

	if rv.Kind() == reflect.Struct {
		fm := mapper.TypeMap(rv.Type())
		fi := fm.GetByPath(name)
		if fi == nil {
			return nil, fmt.Errorf("dba: field '%s' not found in struct", name)
		}
		return reflectx.FieldByIndexesReadOnly(rv, fi.Index).Interface(), nil
	}

	return nil, fmt.Errorf("dba: named args source must be struct or map")
}

// Batch generates parenthesized value groups for bulk INSERT.
func (d *SQL) Batch(rows [][]any) *SQL {
	if len(rows) == 0 {
		clone := d.copy()
		clone.err = errors.New("dba: batch: empty rows")
		return clone
	}

	width := len(rows[0])
	if width == 0 {
		clone := d.copy()
		clone.err = errors.New("dba: batch: empty row")
		return clone
	}

	result := d
	var builder strings.Builder
	builder.Grow(len(rows) * width * 8)
	var bindArgs []any
	argIdx := 1

	for i, row := range rows {
		if len(row) != width {
			clone := d.copy()
			clone.err = fmt.Errorf("dba: batch: row %d has length %d, expected %d", i, len(row), width)
			return clone
		}

		if i > 0 {
			builder.WriteString(", ")
		}

		builder.WriteString("(")
		for j, val := range row {
			if j > 0 {
				builder.WriteString(", ")
			}

			if expr, ok := val.(SQLExpr); ok {
				varName := fmt.Sprintf("__dba_batch_%d_%d_%d", d.copyId, i, j)
				result = result.Var(varName, expr.Sql, expr.Args...)
				builder.WriteString("${" + varName + "}")
			} else {
				builder.WriteString(fmt.Sprintf("#{%d}", argIdx))
				bindArgs = append(bindArgs, val)
				argIdx++
			}
		}
		builder.WriteString(")")
	}

	return result.Add(builder.String(), bindArgs...)
}

// BatchInsert builds a complete INSERT from a slice of entities.
// All entities must have the same column structure.
func (d *SQL) BatchInsert(table string, entities []any) *SQL {
	if len(entities) == 0 {
		clone := d.copy()
		clone.err = errors.New("dba: batch insert: empty entities")
		return clone
	}

	cols, _, err := ExtractColsVals(entities[0])
	if err != nil || len(cols) == 0 {
		clone := d.copy()
		clone.err = fmt.Errorf("dba: batch insert: %w", err)
		return clone
	}

	rows := make([][]any, len(entities))
	for i, entity := range entities {
		m := ToMap(entity)
		row := make([]any, len(cols))
		for j, col := range cols {
			if val, ok := m[col]; !ok {
				clone := d.copy()
				clone.err = fmt.Errorf("dba: batch insert: entity %d missing column %s", i, col)
				return clone
			} else {
				row[j] = val
			}
		}
		rows[i] = row
	}

	quotedCols := make([]string, len(cols))
	for i, col := range cols {
		quotedCols[i] = d.quoter(col)
	}

	insertHead := fmt.Sprintf("INSERT ${%s:} INTO %s (%s) VALUES",
		I, d.quoter(table), strings.Join(quotedCols, ", "))

	return d.Add(insertHead).Batch(rows)
}

// List scans multiple rows into a slice pointer.
func (d *SQL) List(dest interface{}) error {
	if mapSlice, ok := dest.(*[]map[string]any); ok {
		rows, err := d.Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			m := make(map[string]any)
			if err := rows.MapScan(m); err != nil {
				return err
			}
			*mapSlice = append(*mapSlice, m)
		}
		return rows.Err()
	}
	_, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return nil, sqlx.SelectContext(ctx, d.db, dest, query, args...)
	})
	return err
}

// Get scans a single row. Returns (false, nil) when no row is found.
func (d *SQL) Get(dest any) (found bool, err error) {
	if m, ok := dest.(*map[string]any); ok {
		rows, err := d.Rows()
		if err != nil {
			return false, err
		}
		defer rows.Close()

		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return false, err
			}
			return false, nil
		}

		*m = make(map[string]any)

		if err := rows.MapScan(*m); err != nil {
			return false, err
		}
		return true, nil
	}

	_, err = d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return nil, sqlx.GetContext(ctx, d.db, dest, query, args...)
	})

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Exec builds and executes a non-query statement.
func (d *SQL) Exec() (sql.Result, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.ExecContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(sql.Result), nil
}

// Rows returns a raw *sqlx.Rows cursor for streaming.
func (d *SQL) Rows() (*sqlx.Rows, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.QueryxContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(*sqlx.Rows), nil
}

// ToSQL returns the built SQL and arguments without executing.
func (d *SQL) ToSQL() (string, []any, error) {
	return d.build()
}

// Error returns the accumulated error on this builder.
func (d *SQL) Error() error {
	return d.err
}

// Select generates a SELECT statement with ${F:*} for field expansion.
func (d *SQL) Select(table string, where string, args ...any) *SQL {
	return d.Add("SELECT ${"+F+":*} FROM "+d.quoter(table)+" WHERE "+where, args...)
}

// Insert generates and appends an INSERT INTO statement.
func (d *SQL) Insert(table string, data any) *SQL {
	cols, vals, err := ExtractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("dba: insert: %w", err)
		return clone
	}
	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	var bindArgs []any
	prefix := d.copyId
	result := d

	for i, c := range cols {
		quotedCols[i] = d.quoter(c)
		if expr, ok := vals[i].(SQLExpr); ok {
			varName := fmt.Sprintf("__expr_%d_%d", prefix, i)
			placeholders[i] = "${" + varName + "}"
			result = result.Var(varName, expr.Sql, expr.Args...)
		} else {
			bindArgs = append(bindArgs, vals[i])
			placeholders[i] = fmt.Sprintf("#{%d}", len(bindArgs))
		}
	}
	query := fmt.Sprintf("INSERT ${"+I+"} INTO %s (%s) VALUES (%s)",
		d.quoter(table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return result.Add(query, bindArgs...)
}

// Update generates and appends an UPDATE ... SET statement.
func (d *SQL) Update(table string, data any, where string, args ...any) *SQL {
	cols, vals, err := ExtractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("dba: update: %w", err)
		return clone
	}
	setClauses := make([]string, len(cols))
	var bindArgs []any
	prefix := d.copyId
	result := d

	for i, c := range cols {
		if expr, ok := vals[i].(SQLExpr); ok {
			varName := fmt.Sprintf("__expr_%d_%d", prefix, i)
			setClauses[i] = d.quoter(c) + "=${" + varName + "}"
			result = result.Var(varName, expr.Sql, expr.Args...)
		} else {
			bindArgs = append(bindArgs, vals[i])
			setClauses[i] = d.quoter(c) + "=" + fmt.Sprintf("#{%d}", len(bindArgs))
		}
	}
	setQuery := fmt.Sprintf("UPDATE %s SET %s WHERE",
		d.quoter(table),
		strings.Join(setClauses, ", "),
	)
	return result.Add(setQuery, bindArgs...).Add(where, args...)
}

// Delete generates and appends a DELETE FROM statement.
func (d *SQL) Delete(table string, where string, args ...any) *SQL {
	return d.Add(fmt.Sprintf("DELETE FROM %s WHERE", d.quoter(table))).Add(where, args...)
}

// Begin starts a transaction and returns a new builder backed by the Tx.
func (d *SQL) Begin() (*SQL, error) {
	if d.rawDB == nil {
		return nil, errors.New("dba: transaction already started")
	}
	tx, err := d.rawDB.BeginTxx(d.ctx, nil)
	if err != nil {
		return nil, err
	}
	clone := d.copy()
	clone.db = tx
	clone.rawDB = nil
	return clone, nil
}

// Commit commits the active transaction.
func (d *SQL) Commit() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("dba: no active transaction")
	}
	return tx.Commit()
}

// Rollback rolls back the active transaction.
func (d *SQL) Rollback() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("dba: no active transaction")
	}
	return tx.Rollback()
}

// Transaction executes fn inside a transaction. If fn returns an error or
// panics, the transaction is rolled back. If already in a transaction, fn
// is executed directly without nesting.
func (d *SQL) Transaction(fn func(*SQL) error) error {
	if d.rawDB == nil {
		return fn(d)
	}
	txDuck, err := d.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = txDuck.Rollback() }()

	if err := fn(txDuck); err != nil {
		return err
	}
	return txDuck.Commit()
}
