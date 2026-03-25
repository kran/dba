package sqlo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx/reflectx"
	"reflect"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

const (
	F = "F"
	I = "I"
)

type H = map[string]any

var mapper = reflectx.NewMapperFunc("db", strings.ToLower)

// Expr 表示一个原始 SQL 表达式，用于 Insert/Update 中需要非绑定值的场景
type Expr struct {
	SQL  string
	Args []any
}

func NewExpr(sql string, args ...any) Expr {
	return Expr{SQL: sql, Args: args}
}

// Middleware 中间件类型，洋葱模型
type Middleware func(next ExecFunc) ExecFunc
type ExecFunc func(ctx context.Context, query string, args []any) (any, error)

// PlaceholderFormat 用于多库兼容 (PG的 $1, MySQL的 ?)
type PlaceholderFormat func(idx int) string

func QuestionMarkFormat(_ int) string { return "?" }
func DollarFormat(idx int) string     { return "$" + strconv.Itoa(idx) }

// Quoter 用于 @{} 宏的标识符安全转义 (比如把 user 转为 `user` 或 "user")
type Quoter func(string) string

func MySQLQuoter(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func AnsiQuoter(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

type node struct {
	rawSQL string
	args   []any
}

// Sqlo the builder
type Sqlo struct {
	mainNodes []node          // 主干节点
	varNodes  map[string]node // 宏变量节点 (由 Var 注册)

	db          sqlx.ExtContext
	rawDB       *sqlx.DB
	ctx         context.Context
	err         error
	quoter      Quoter
	format      PlaceholderFormat
	driverName  string
	middlewares []Middleware
}

// New 包装现有的 sqlx.DB
func New(db *sqlx.DB) *Sqlo {
	driver := db.DriverName()
	quoter := AnsiQuoter
	format := QuestionMarkFormat

	if driver == "postgres" || driver == "pgx" || driver == "pq" {
		quoter = AnsiQuoter
		format = DollarFormat
	} else if driver == "mysql" {
		quoter = MySQLQuoter
		format = QuestionMarkFormat
	}

	return &Sqlo{
		mainNodes:  make([]node, 0),
		varNodes:   make(map[string]node),
		db:         db,
		rawDB:      db,
		ctx:        context.Background(),
		quoter:     quoter,
		format:     format,
		driverName: driver,
	}
}

// copy 实现不可变模式 (Immutable)
func (d *Sqlo) copy() *Sqlo {
	clone := &Sqlo{
		mainNodes:  make([]node, len(d.mainNodes)),
		varNodes:   make(map[string]node),
		db:         d.db,
		rawDB:      d.rawDB,
		ctx:        d.ctx,
		err:        d.err,
		quoter:     d.quoter,
		format:     d.format,
		driverName: d.driverName,
	}
	copy(clone.mainNodes, d.mainNodes)
	for k, v := range d.varNodes {
		clone.varNodes[k] = v
	}
	if len(d.middlewares) > 0 {
		clone.middlewares = make([]Middleware, len(d.middlewares))
		copy(clone.middlewares, d.middlewares)
	}
	return clone
}

// WithContext 设置上下文
func (d *Sqlo) WithContext(ctx context.Context) *Sqlo {
	clone := d.copy()
	clone.ctx = ctx
	return clone
}

// Quoter change quoter
func (d *Sqlo) Quoter(quoter Quoter) *Sqlo {
	clone := d.copy()
	clone.quoter = quoter
	return clone
}

// Format change placeholder format
func (d *Sqlo) Format(formatter PlaceholderFormat) *Sqlo {
	clone := d.copy()
	clone.format = formatter
	return clone
}

// Unsafe 返回一个忽略未映射列的 Sqlo（不报 "missing destination" 错误）
func (d *Sqlo) Unsafe() *Sqlo {
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

// Use 添加中间件，返回新实例
func (d *Sqlo) Use(mw ...Middleware) *Sqlo {
	clone := d.copy()
	clone.middlewares = append(clone.middlewares, mw...)
	return clone
}

// execute 构建 SQL 并通过中间件链执行
func (d *Sqlo) execute(fn ExecFunc) (any, error) {
	query, args, err := d.build()
	if err != nil {
		return nil, err
	}
	exec := fn
	for i := len(d.middlewares) - 1; i >= 0; i-- {
		exec = d.middlewares[i](exec)
	}
	return exec(d.ctx, query, args)
}

// Add 核心方法：添加 SQL 片段并解析宏
func (d *Sqlo) Add(query string, args ...any) *Sqlo {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.mainNodes = append(clone.mainNodes, node{rawSQL: query, args: args})
	return clone
}

// AddIf 条件拼接
func (d *Sqlo) AddIf(cond bool, query string, args ...any) *Sqlo {
	if cond {
		return d.Add(query, args...)
	}
	return d
}

// Var 注册局部宏变量，替代原有的 Mark
func (d *Sqlo) Var(key string, query string, args ...any) *Sqlo {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.varNodes[key] = node{rawSQL: query, args: args}
	return clone
}

func (d *Sqlo) build() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}

	var sqlBuilder strings.Builder
	var finalArgs []any
	argCount := 0 // 全局参数索引，用于生成 $1, $2 等

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

			// 【转义拦截逻辑】：如果看到 \${, \#{, \@{, \!{
			if start >= 2 && str[start-2] == '\\' && (str[start-1] == '#' || str[start-1] == '$' || str[start-1] == '@' || str[start-1] == '!') {
				writeText(str[i : start-2])                    // 写入 \ 之前的内容
				sqlBuilder.WriteString(str[start-1 : start+1]) // 写入如 "#{", 不解析
				i = start + 1
				continue
			}

			// 检查有效前缀
			if start == 0 || (str[start-1] != '#' && str[start-1] != '$' && str[start-1] != '@' && str[start-1] != '!') {
				writeText(str[i : start+1])
				i = start + 1
				continue
			}

			prefix := str[start-1]
			writeText(str[i : start-1])

			end := strings.IndexByte(str[start:], '}')
			if end == -1 {
				return fmt.Errorf("sqlo: unclosed brace in %q", str)
			}
			end += start
			content := str[start+1 : end]

			switch prefix {
			case '#': // 参数绑定 #{1} 或 #{name}
				argVal, err := resolveArg(n.args, content)
				if err != nil {
					return err
				}

				// 切片自动展开 (干掉 sqlx.In)
				rv := reflect.ValueOf(argVal)
				for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
					if rv.IsNil() {
						break
					}
					rv = rv.Elem() // 完美解引用
				}

				if argVal != nil && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
					if rv.Len() == 0 {
						return fmt.Errorf("sqlo: empty slice passed to parameter #{%s}", content)
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

			case '$': // 模板宏 ${key:default} 递归展开
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

			case '@': // 标识符安全转义 @{1} 或 @{name}
				val, err := resolveArg(n.args, content)
				if err != nil {
					return err
				} else if val == nil {
					return fmt.Errorf("sqlo: @{%s} resolved to nil", content)
				}
				sqlBuilder.WriteString(d.quoter(fmt.Sprintf("%v", val)))

			case '!': // 纯文本原样输出 !{1} 或 !{name}
				val, err := resolveArg(n.args, content)
				if err != nil {
					return err
				} else if val == nil {
					return fmt.Errorf("sqlo: !{%s} resolved to nil", content)
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

// resolveArg 根据 content 尝试解析为位置参数或命名参数
func resolveArg(args []any, content string) (any, error) {
	if idx, err := strconv.Atoi(content); err == nil {
		if idx < 1 || idx > len(args) {
			return nil, fmt.Errorf("index %d out of bounds", idx)
		}
		return args[idx-1], nil
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("no args")
	}
	return extractNamedArg(args[len(args)-1], content)
}

// extractNamedArg 辅助函数：通过反射获取命名参数
func extractNamedArg(src any, name string) (any, error) {
	rv := reflect.ValueOf(src)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, fmt.Errorf("sqlo: named args source is nil pointer")
		}
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("sqlo: map key must be string")
		}
		val := rv.MapIndex(reflect.ValueOf(name))
		if !val.IsValid() {
			return nil, fmt.Errorf("sqlo: named arg '%s' not found in map", name)
		}
		return val.Interface(), nil
	}

	if rv.Kind() == reflect.Struct {
		fm := mapper.TypeMap(rv.Type())
		fi := fm.GetByPath(name)
		if fi == nil {
			return nil, fmt.Errorf("sqlo: field '%s' not found in struct", name)
		}
		return reflectx.FieldByIndexes(rv, fi.Index).Interface(), nil
	}

	return nil, fmt.Errorf("sqlo: named args source must be struct or map")
}

// Batch 生成 VALUES (?,?,...), (?,?,...) 用于批量 INSERT
func (d *Sqlo) Batch(rows [][]any) *Sqlo {
	if len(rows) == 0 {
		clone := d.copy()
		clone.err = errors.New("sqlo batch: empty rows")
		return clone
	}

	width := len(rows[0])
	if width == 0 {
		clone := d.copy()
		clone.err = errors.New("sqlo batch: empty row")
		return clone
	}

	var builder strings.Builder
	builder.Grow(len(rows) * width * 8)
	args := make([]any, 0, len(rows)*width)
	argIdx := 1

	for i, row := range rows {
		if len(row) != width {
			clone := d.copy()
			clone.err = fmt.Errorf("sqlo batch: row %d has length %d, expected %d", i, len(row), width)
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
			builder.WriteString(fmt.Sprintf("#{%d}", argIdx))
			args = append(args, val)
			argIdx++
		}
		builder.WriteString(")")
	}

	return d.Add(builder.String(), args...)
}

// List 映射多行到 Slice，dest 可以是 *[]SomeStruct 或 *[]map[string]any
func (d *Sqlo) List(dest interface{}) error {
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

// Get 映射单行到 Struct，dest 可以是 *SomeStruct 或 *map[string]any
func (d *Sqlo) Get(dest any) (found bool, err error) {
	if m, ok := dest.(*map[string]any); ok {
		rows, err := d.Rows()
		if err != nil {
			return false, err
		}
		defer rows.Close()

		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return false, err // 真正的游标异常
			}
			return false, nil
		}

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

// Exec 执行非查询 SQL
func (d *Sqlo) Exec() (sql.Result, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.ExecContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(sql.Result), nil
}

// Rows 返回原始 *sqlx.Rows，用于流式处理大结果集
func (d *Sqlo) Rows() (*sqlx.Rows, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.QueryxContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(*sqlx.Rows), nil
}

// ToSQL 返回最终 SQL 和参数，不执行，用于调试和日志
func (d *Sqlo) ToSQL() (string, []any, error) {
	return d.build()
}

// Error 返回 builder 累积的错误
func (d *Sqlo) Error() error {
	return d.err
}

func (d *Sqlo) Select(table string, where string, args ...any) *Sqlo {
	return d.Add("SELECT ${"+F+":*} FROM "+d.quoter(table)+" WHERE "+where, args...)
}

// Insert 生成并追加 INSERT INTO 语句
func (d *Sqlo) Insert(table string, data any) *Sqlo {
	cols, vals, err := ExtractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("insert: %w", err)
		return clone
	}
	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	var bindArgs []any
	result := d

	for i, c := range cols {
		quotedCols[i] = d.quoter(c)
		if expr, ok := vals[i].(Expr); ok {
			varName := fmt.Sprintf("__expr_%d", i)
			placeholders[i] = "${" + varName + "}"
			result = result.Var(varName, expr.SQL, expr.Args...)
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

// Update 生成并追加 UPDATE ... SET 语句
func (d *Sqlo) Update(table string, data any, where string, args ...any) *Sqlo {
	cols, vals, err := ExtractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("update: %w", err)
		return clone
	}
	setClauses := make([]string, len(cols))
	var bindArgs []any
	result := d

	for i, c := range cols {
		if expr, ok := vals[i].(Expr); ok {
			varName := fmt.Sprintf("__expr_%d", i)
			setClauses[i] = d.quoter(c) + "=${" + varName + "}"
			result = result.Var(varName, expr.SQL, expr.Args...)
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

// Delete 生成并执行 DELETE FROM 语句
func (d *Sqlo) Delete(table string, where string, args ...any) *Sqlo {
	return d.Add(fmt.Sprintf("DELETE FROM %s WHERE", d.quoter(table))).Add(where, args...)
}

// ==========================================
// 事务支持
// ==========================================

// Begin 开启事务，返回携带事务状态的新 Sqlo 实例
func (d *Sqlo) Begin() (*Sqlo, error) {
	if d.rawDB == nil {
		return nil, errors.New("transaction already started")
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

// Commit 提交事务
func (d *Sqlo) Commit() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("no active transaction")
	}
	return tx.Commit()
}

// Rollback 回滚事务
func (d *Sqlo) Rollback() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("no active transaction")
	}
	return tx.Rollback()
}

// Transaction 闭包事务：fn 返回 error 或发生 panic 时自动回滚。
// 若当前已在事务中，直接执行 fn（join 外层事务），不开新事务。
func (d *Sqlo) Transaction(fn func(*Sqlo) error) error {
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
