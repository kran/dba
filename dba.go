package dba

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

// F is the default field variable name used by Select and Page.
const (
	F = "F"
	I = "I"
)

// H is a shorthand for map[string]any.
type H = map[string]any

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

// Formater generates a placeholder for the n-th parameter.
type Formater func(idx int) string

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

type Node struct {
	RawSQL string
	Args   []any
}

// SQL is an immutable, chainable query builder backed by sqlx.
type SQL struct {
	mainNodes []Node
	varNodes  map[string]Node
	pipes     map[string]Pipe // 管道注册表 (实例级, copy-on-write)
	macros    map[byte]string // 宏前缀 → 管道名 (实例级, copy-on-write)

	executor   sqlx.ExtContext
	pool       *sqlx.DB
	ctx        context.Context
	err        error
	quoter     Quoter
	formater   Formater
	driverName string
	hooks      []Hook
	copyId     int
}

// NewFromSqlx creates a SQL builder from a sqlx.DB. Auto-detects driver for
// placeholder formater and identifier quoting.
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
		mainNodes:  make([]Node, 0),
		varNodes:   make(map[string]Node),
		pipes:      maps.Clone(defaultPipes),
		macros:     maps.Clone(defaultMacros),
		executor:   db,
		pool:       db,
		ctx:        context.Background(),
		quoter:     quoter,
		formater:   format,
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

func (d *SQL) Pool() *sqlx.DB { return d.pool }

func (d *SQL) Close() error {
	if d.pool != nil {
		return d.pool.Close()
	}
	return nil
}

func (d *SQL) copy() *SQL {
	clone := &SQL{
		mainNodes:  make([]Node, len(d.mainNodes)),
		varNodes:   make(map[string]Node),
		executor:   d.executor,
		pool:       d.pool,
		ctx:        d.ctx,
		err:        d.err,
		quoter:     d.quoter,
		formater:   d.formater,
		driverName: d.driverName,
		copyId:     d.copyId + 1,
	}
	copy(clone.mainNodes, d.mainNodes)
	// 低频写字段: 共享只读, 写操作 (Var/Vars/Use/Register*) 各自 copy-on-write
	clone.varNodes = d.varNodes
	clone.pipes = d.pipes
	clone.macros = d.macros
	clone.hooks = d.hooks
	return clone
}

// WithCtx returns a new builder with the given context.
func (d *SQL) WithCtx(ctx context.Context) *SQL {
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

// Formater returns a new builder with the given placeholder formater.
func (d *SQL) Formater(formatter Formater) *SQL {
	clone := d.copy()
	clone.formater = formatter
	return clone
}

// Unsafe returns a new builder that ignores unmapped columns.
func (d *SQL) Unsafe() *SQL {
	clone := d.copy()
	switch v := d.executor.(type) {
	case *sqlx.DB:
		clone.executor = v.Unsafe()
		if d.pool != nil {
			clone.pool = d.pool.Unsafe()
		}
	case *sqlx.Tx:
		clone.executor = v.Unsafe()
	}
	return clone
}

// Use returns a new builder with the given hooks appended.
func (d *SQL) Use(mw ...Hook) *SQL {
	clone := d.copy()
	clone.hooks = append(append([]Hook{}, clone.hooks...), mw...) // copy-on-write
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
	clone.mainNodes = append(clone.mainNodes, Node{RawSQL: query, Args: args})
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
	clone.varNodes = maps.Clone(clone.varNodes) // copy-on-write
	clone.varNodes[key] = Node{RawSQL: query, Args: args}
	return clone
}

func (d *SQL) Vars(vars map[string]Node) *SQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.varNodes = maps.Clone(clone.varNodes) // copy-on-write
	for k, v := range vars {
		clone.varNodes[k] = v
	}
	return clone
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

	// 批量要求所有行同列集: omitempty 按行跳列会导致列集漂移 (零值行缺列),
	// 统一用全列 (omitempty=false), 零值也插入。
	cols, _, err := ColumnsAndValues(entities[0], false)
	if err != nil || len(cols) == 0 {
		clone := d.copy()
		clone.err = fmt.Errorf("dba: batch insert: %w", err)
		return clone
	}

	rows := make([][]any, len(entities))
	for i, entity := range entities {
		keys, vals, err := ColumnsAndValues(entity, false)
		if err != nil {
			clone := d.copy()
			clone.err = fmt.Errorf("dba: batch insert: entity %d: %w", i, err)
			return clone
		}

		m := make(map[string]any, len(keys))
		for j, k := range keys {
			m[k] = vals[j]
		}

		row := make([]any, len(cols))
		for j, col := range cols {
			val, ok := m[col]
			if !ok {
				clone := d.copy()
				clone.err = fmt.Errorf("dba: batch insert: entity %d missing column %s", i, col)
				return clone
			}
			row[j] = val
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
		return nil, sqlx.SelectContext(ctx, d.executor, dest, query, args...)
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
		return nil, sqlx.GetContext(ctx, d.executor, dest, query, args...)
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
		return d.executor.ExecContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(sql.Result), nil
}

// Rows returns a raw *sqlx.Rows cursor for streaming.
func (d *SQL) Rows() (*sqlx.Rows, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.executor.QueryxContext(ctx, query, args...)
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
	cols, vals, err := ColumnsAndValues(data, true)
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
	query := fmt.Sprintf("INSERT ${"+I+":} INTO %s (%s) VALUES (%s)",
		d.quoter(table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return result.Add(query, bindArgs...)
}

// Update generates and appends an UPDATE ... SET statement.
func (d *SQL) Update(table string, data any, where string, args ...any) *SQL {
	cols, vals, err := ColumnsAndValues(data, true)
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
	if d.pool == nil {
		return nil, errors.New("dba: transaction already started")
	}
	tx, err := d.pool.BeginTxx(d.ctx, nil)
	if err != nil {
		return nil, err
	}
	clone := d.copy()
	clone.executor = tx
	clone.pool = nil
	return clone, nil
}

// Commit commits the active transaction.
func (d *SQL) Commit() error {
	tx, ok := d.executor.(*sqlx.Tx)
	if !ok {
		return errors.New("dba: no active transaction")
	}
	return tx.Commit()
}

// Rollback rolls back the active transaction.
func (d *SQL) Rollback() error {
	tx, ok := d.executor.(*sqlx.Tx)
	if !ok {
		return errors.New("dba: no active transaction")
	}
	return tx.Rollback()
}

// Transaction executes fn inside a transaction. If fn returns an error or
// panics, the transaction is rolled back. If already in a transaction, fn
// is executed directly without nesting.
func (d *SQL) Transaction(fn func(*SQL) error) error {
	// already in tx
	if d.pool == nil {
		return fn(d)
	}

	tx, err := d.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback()
			panic(r)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (d *SQL) build() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}

	var sqlBuilder strings.Builder
	var finalArgs []any
	argCount := 0

	sqlBuilder.Grow(512)

	// 渲染: token 列表 → SQL 文本 + 参数 (扫描已由 lex 完成)
	bw := &buildRenderCtx{sqlBuilder: &sqlBuilder, argCount: &argCount, formater: d.formater, finalArgs: &finalArgs, quoter: d.quoter}
	var render func(items []item, args []any) error
	render = func(items []item, args []any) error {
		prev := bw.args
		bw.args = args                    // 当前参数列表 (var 递归时 = varNode.Args)
		defer func() { bw.args = prev }() // 递归返回后恢复, 外层宏继续用外层参数
		for _, it := range items {
			switch it.kind {
			case itemText:
				sqlBuilder.WriteString(it.val)

			case itemMacro:
				if err := d.renderMacro(bw, render, it, args); err != nil {
					return err
				}
			default:
				// itemError 已被 lex 过滤, 此处兜底未来扩展的 token 类型
				return fmt.Errorf("dba: unexpected token kind %d", it.kind)
			}
		}
		return nil
	}

	for i, node := range d.mainNodes {
		if i > 0 {
			sqlBuilder.WriteString("\n")
		}
		items, err := lex(node.RawSQL, d.macros)
		if err != nil {
			return "", nil, err
		}
		if err := render(items, node.Args); err != nil {
			return "", nil, err
		}
	}

	return sqlBuilder.String(), finalArgs, nil
}

// renderMacro 渲染宏 token: '#' = 管道容器, '$' = 变量 (结构层), 其他 = 宏别名。
func (d *SQL) renderMacro(bw RenderCtx, render func([]item, []any) error, it item, args []any) error {
	// $ 变量: 结构层 (模板递归), 不经管道
	if it.prefix == '$' {
		parts := strings.SplitN(it.val, ":", 2)
		key := strings.TrimSpace(parts[0])
		if varNode, ok := d.varNodes[key]; ok {
			sub, err := lex(varNode.RawSQL, d.macros)
			if err != nil {
				return err
			}
			return render(sub, varNode.Args)
		}
		if len(parts) == 2 {
			sub, err := lex(strings.TrimSpace(parts[1]), d.macros)
			if err != nil {
				return err
			}
			return render(sub, nil)
		}
		return fmt.Errorf("dba: undefined variable ${%s}", key)
	}

	// 统一管道渲染: 所有宏 (#/@/!/用户宏) 同一逻辑。
	// 内容 "key|pipe" 可覆盖管道; 否则用宏默认管道; 再否则 bind。
	key, pipe, hasPipe := splitKeyPipe(it.val)
	if !hasPipe {
		pipe = d.macros[it.prefix]
		if pipe == "" {
			pipe = "bind"
		}
	}
	fn, ok := d.pipes[pipe]
	if !ok {
		return fmt.Errorf("dba: unknown pipe %q", pipe)
	}
	return fn(bw, key)
}

// splitKeyPipe 分割 "key|pipe" 内容; 两端空白容忍 (与 $ 变量一致)。
// hasPipe=false 表示内容未声明管道 (用宏默认或 bind)。
func splitKeyPipe(content string) (key, pipe string, hasPipe bool) {
	if idx := strings.IndexByte(content, '|'); idx >= 0 {
		return strings.TrimSpace(content[:idx]), strings.TrimSpace(content[idx+1:]), true
	}
	return strings.TrimSpace(content), "bind", false
}
