package stupidql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx/reflectx"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

const (
	F = "@fields"
	I = "@ignore"
)

type H = map[string]any

// Middleware 中间件类型，洋葱模型
type ExecFunc func(ctx context.Context, query string, args []any) (any, error)
type Middleware func(next ExecFunc) ExecFunc

// Expr 表示一个原始 SQL 表达式，用于 Insert/Update 中需要非绑定值的场景
type Expr struct {
	SQL  string
	Args []any
}

var (
	formatPhPattern = regexp.MustCompile(`([#@!])\{([^}]+?)}`)
	intPattern      = regexp.MustCompile(`^\d+$`)
)

var mapper = reflectx.NewMapperFunc("db", strings.ToLower)

// Quoter 用于 @{} 宏的标识符安全转义 (比如把 user 转为 `user` 或 "user")
type Quoter func(string) string

func MySQLQuoter(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func AnsiQuoter(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// StupidQL 是一个不可变的动态 SQL 拼装引擎
type StupidQL struct {
	queries []string
	args    [][]any
	marks   map[string]int

	db          sqlx.ExtContext // sqlx 的核心接口，兼容 *sqlx.DB 和 *sqlx.Tx
	rawDB       *sqlx.DB        // 仅用于开启事务，进入事务后为 nil
	ctx         context.Context
	err         error
	quoter      Quoter
	driverName  string
	middlewares []Middleware
}

// NewStupidQL 包装现有的 sqlx.DB
func NewStupidQL(db *sqlx.DB) *StupidQL {
	quoter := AnsiQuoter // 默认使用 PG 引号
	if db.DriverName() == "mysql" {
		quoter = MySQLQuoter
	}
	return &StupidQL{
		queries:    make([]string, 0),
		args:       make([][]any, 0),
		marks:      make(map[string]int),
		db:         db,
		rawDB:      db,
		ctx:        context.Background(),
		quoter:     quoter,
		driverName: db.DriverName(),
	}
}

func NewExpr(sql string, args ...any) Expr {
	return Expr{SQL: sql, Args: args}
}

// Scalar 泛型函数，获取单个标量值（如 COUNT、MAX、单列查询等）
func Scalar[T any](d *StupidQL) (T, error) {
	var v T
	err := d.Get(&v)
	return v, err
}

// Page 泛型分页查询，要求 q 使用 Mark(F, ...) 标记 SELECT 字段
// 内部用 COUNT(1) 替换 F 查总数，原 query 加 LIMIT/OFFSET 查数据
func Page[T any](q *StupidQL, page, size int) ([]T, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}

	if _, ok := q.marks[F]; !ok {
		return nil, 0, fmt.Errorf("page requires Mark(F, ...) in query")
	}

	// 查总数：替换 F 为 COUNT(1)
	total, err := Scalar[int64](q.Mark(F, "COUNT(1)"))
	if err != nil || total == 0 {
		return nil, total, err
	}

	// 查数据
	var items []T
	offset := (page - 1) * size
	err = q.Add("LIMIT #{1} OFFSET #{2}", size, offset).List(&items)
	return items, total, err
}

// copy 实现不可变模式 (Immutable)
func (d *StupidQL) copy() *StupidQL {
	clone := &StupidQL{
		marks:      make(map[string]int),
		queries:    make([]string, len(d.queries)),
		args:       make([][]any, len(d.args)),
		db:         d.db,
		rawDB:      d.rawDB,
		ctx:        d.ctx,
		err:        d.err,
		quoter:     d.quoter,
		driverName: d.driverName,
	}
	copy(clone.queries, d.queries)
	copy(clone.args, d.args)
	for k, v := range d.marks {
		clone.marks[k] = v
	}
	if len(d.middlewares) > 0 {
		clone.middlewares = make([]Middleware, len(d.middlewares))
		copy(clone.middlewares, d.middlewares)
	}
	return clone
}

// WithContext 设置上下文
func (d *StupidQL) WithContext(ctx context.Context) *StupidQL {
	clone := d.copy()
	clone.ctx = ctx
	return clone
}

// Unsafe 返回一个忽略未映射列的 StupidQL（不报 "missing destination" 错误）
func (d *StupidQL) Unsafe() *StupidQL {
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
func (d *StupidQL) Use(mw ...Middleware) *StupidQL {
	clone := d.copy()
	clone.middlewares = append(clone.middlewares, mw...)
	return clone
}

// execute 构建 SQL 并通过中间件链执行
func (d *StupidQL) execute(fn ExecFunc) (any, error) {
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
func (d *StupidQL) Add(query string, args ...any) (clone *StupidQL) {
	clone = d.copy()
	if clone.err != nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			clone.err = fmt.Errorf("stupidql add panic: %v", r)
		}
	}()

	parsedQuery, parsedArgs := clone.parseText(query, args...)
	clone.queries = append(clone.queries, parsedQuery)
	clone.args = append(clone.args, parsedArgs)
	return
}

// AddIf 条件拼接
func (d *StupidQL) AddIf(cond bool, query string, args ...any) *StupidQL {
	if cond {
		return d.Add(query, args...)
	}
	return d
}

// Mark 预留或替换命名占位符
func (d *StupidQL) Mark(name string, query string, args ...any) (clone *StupidQL) {
	clone = d.copy()
	if clone.err != nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			clone.err = fmt.Errorf("stupidql mark panic: %v", r)
		}
	}()

	parsedQuery, parsedArgs := clone.parseText(query, args...)

	if idx, ok := clone.marks[name]; ok {
		clone.queries[idx] = parsedQuery
		clone.args[idx] = parsedArgs
	} else {
		clone.marks[name] = len(clone.queries)
		clone.queries = append(clone.queries, parsedQuery)
		clone.args = append(clone.args, parsedArgs)
	}
	return
}

// build 是连接 sqlx 的核心桥梁
func (d *StupidQL) build() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}

	// 1. 拼接所有片段
	query := strings.Join(d.queries, "\n")
	var flatArgs []any
	for _, args := range d.args {
		flatArgs = append(flatArgs, args...)
	}

	qmark := "\x00stupidql-qmark\x00"
	query = strings.ReplaceAll(query, "??", qmark)
	query, flatArgs, err := sqlx.In(query, flatArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("sqlx.In expand failed: %w", err)
	}

	query = d.db.Rebind(query)
	query = strings.ReplaceAll(query, qmark, "?")

	return query, flatArgs, nil
}

// ==========================================
// 委托给 sqlx 执行的终端方法
// ==========================================

// Batch 生成 VALUES (?,?,...), (?,?,...) 用于批量 INSERT
func (d *StupidQL) Batch(rows [][]any) *StupidQL {
	if len(rows) == 0 {
		clone := d.copy()
		clone.err = errors.New("batch: empty rows")
		return clone
	}
	width := len(rows[0])
	if width == 0 {
		clone := d.copy()
		clone.err = errors.New("batch: empty row")
		return clone
	}
	placeholder := "(" + strings.Repeat("?,", width-1) + "?)"
	placeholders := make([]string, len(rows))
	for i := range rows {
		placeholders[i] = placeholder
	}
	var args []any
	for _, row := range rows {
		args = append(args, row...)
	}
	return d.Add("VALUES "+strings.Join(placeholders, ", "), args...)
}

// List 映射多行到 Slice，dest 可以是 *[]SomeStruct 或 *[]map[string]any
func (d *StupidQL) List(dest interface{}) error {
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
func (d *StupidQL) Get(dest interface{}) error {
	if m, ok := dest.(*map[string]any); ok {
		rows, err := d.Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		return rows.MapScan(*m)
	}
	_, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return nil, sqlx.GetContext(ctx, d.db, dest, query, args...)
	})
	return err
}

// Exec 执行非查询 SQL
func (d *StupidQL) Exec() (sql.Result, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.ExecContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(sql.Result), nil
}

// Rows 返回原始 *sqlx.Rows，用于流式处理大结果集
func (d *StupidQL) Rows() (*sqlx.Rows, error) {
	result, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return d.db.QueryxContext(ctx, query, args...)
	})
	if err != nil {
		return nil, err
	}
	return result.(*sqlx.Rows), nil
}

// ToSQL 返回最终 SQL 和参数，不执行，用于调试和日志
func (d *StupidQL) ToSQL() (string, []any, error) {
	return d.build()
}

// Error 返回 builder 累积的错误
func (d *StupidQL) Error() error {
	return d.err
}

func (d *StupidQL) Select(table string, where string, args ...any) *StupidQL {
	return d.Add("SELECT").
		Mark(F, "*").
		Add("FROM "+d.quoter(table)+" WHERE "+where, args)
}

// Insert 生成并追加 INSERT INTO 语句
// data 支持 struct（读取 db tag，无 tag 则用字段名）或 map[string]any
// 值为 Expr 类型时使用原始 SQL 表达式替代绑定参数
func (d *StupidQL) Insert(table string, data any) *StupidQL {
	cols, vals, err := extractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("insert: %w", err)
		return clone
	}
	quotedCols := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	var bindArgs []any
	for i, c := range cols {
		quotedCols[i] = d.quoter(c)
		if expr, ok := vals[i].(Expr); ok {
			parsedSQL, parsedArgs := d.parseText(expr.SQL, expr.Args...)
			placeholders[i] = parsedSQL
			bindArgs = append(bindArgs, parsedArgs...)
		} else {
			placeholders[i] = "?"
			bindArgs = append(bindArgs, vals[i])
		}
	}
	query := fmt.Sprintf("INTO %s (%s) VALUES (%s)",
		d.quoter(table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return d.Add("INSERT").Mark(I, "").Add(query, bindArgs...)
}

// Update 生成并追加 UPDATE ... SET 语句
// data 支持 struct（读取 db tag，无 tag 则用字段名）或 map[string]any
// 值为 Expr 类型时使用原始 SQL 表达式替代绑定参数
func (d *StupidQL) Update(table string, data any, where string, args ...any) *StupidQL {
	cols, vals, err := extractColsVals(data)
	if err != nil {
		clone := d.copy()
		clone.err = fmt.Errorf("update: %w", err)
		return clone
	}
	setClauses := make([]string, len(cols))
	var bindArgs []any
	for i, c := range cols {
		if expr, ok := vals[i].(Expr); ok {
			parsedSQL, parsedArgs := d.parseText(expr.SQL, expr.Args...)
			setClauses[i] = d.quoter(c) + "=" + parsedSQL
			bindArgs = append(bindArgs, parsedArgs...)
		} else {
			setClauses[i] = d.quoter(c) + "=?"
			bindArgs = append(bindArgs, vals[i])
		}
	}
	query := fmt.Sprintf("UPDATE %s SET %s WHERE",
		d.quoter(table),
		strings.Join(setClauses, ", "),
	)
	return d.Add(query, bindArgs...).Add(where, args...)
}

// Delete 生成并执行 DELETE FROM 语句
func (d *StupidQL) Delete(table string, where string, args ...any) *StupidQL {
	return d.Add(fmt.Sprintf("DELETE FROM %s WHERE", d.quoter(table))).Add(where, args...)
}

// ==========================================
// 事务支持
// ==========================================

// Begin 开启事务，返回携带事务状态的新 StupidQL 实例
func (d *StupidQL) Begin() (*StupidQL, error) {
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
func (d *StupidQL) Commit() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("no active transaction")
	}
	return tx.Commit()
}

// Rollback 回滚事务
func (d *StupidQL) Rollback() error {
	tx, ok := d.db.(*sqlx.Tx)
	if !ok {
		return errors.New("no active transaction")
	}
	return tx.Rollback()
}

// Transaction 闭包事务：fn 返回 error 或发生 panic 时自动回滚。
// 若当前已在事务中，直接执行 fn（join 外层事务），不开新事务。
func (d *StupidQL) Transaction(fn func(*StupidQL) error) error {
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

// IsOk 检查传入的值是否为 "ok" (非 nil, 非空白字符串, 非空集合/数组/字典)
func IsOk(v any) bool {
	if v == nil {
		return false
	}

	// 常用类型的快速路径 (Type Switch) - 提升性能，避免反射开销
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) != ""
	}

	// 借助反射处理动态类型 (Slice, Array, Map, 指针, 自定义别名等)
	rv := reflect.ValueOf(v)

	// 解引用：处理指针和接口的嵌套情况
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}

	// 根据具体的底层数据结构（Kind）进行判断
	switch rv.Kind() {
	case reflect.String:
		// 处理类似 `type MyString string` 这种自定义别名类型
		return strings.TrimSpace(rv.String()) != ""
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() > 0
	case reflect.Invalid:
		// 防御性兜底，通常在 v 是彻底的 nil 时会走到这里
		return false
	default:
		return true
	}
}

func (d *StupidQL) parseText(tpl string, args ...any) (string, []any) {
	if !strings.Contains(tpl, "{") {
		return tpl, args
	}

	var sqlParams []any
	var namedArgs map[string]any

	replacedTpl := formatPhPattern.ReplaceAllStringFunc(tpl, func(match string) string {
		subMatches := formatPhPattern.FindStringSubmatch(match)
		identity := subMatches[1]
		key := strings.TrimSpace(subMatches[2])

		var arg any

		// 位置索引 #{1}
		if intPattern.MatchString(key) {
			idx, _ := strconv.Atoi(key)
			if idx < 1 || idx > len(args) {
				panic(fmt.Sprintf("Index %d out of range", idx))
			}
			arg = args[idx-1]
		} else {
			// 命名参数 #{name}
			if namedArgs == nil {
				if len(args) == 0 {
					panic("Named argument required but args is empty")
				}
				namedArgs = toMap(args[len(args)-1]) // 取最后一个参数作为数据源
			}
			val, ok := namedArgs[key]
			if !ok {
				panic(fmt.Sprintf("Named argument '%s' not found", key))
			}
			arg = val
		}

		// 宏替换逻辑
		switch identity {
		case "!": // 原生替换 (注意注入风险)
			return fmt.Sprintf("%v", arg)
		case "@": // 标识符转义 (安全表名/列名)
			return d.quoter(fmt.Sprintf("%v", arg))
		case "#": // 安全的参数绑定
			sqlParams = append(sqlParams, arg)
			return "?" // 统一替换为 ?, 后续交由 sqlx.Rebind 处理方言
		}
		return match
	})

	return replacedTpl, sqlParams
}

// toMap 极简版的反射转换为 map
func toMap(model any) map[string]any {
	if m, ok := model.(map[string]any); ok {
		return m
	}

	rv := reflect.ValueOf(model)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return make(map[string]any) // 空指针直接返回空 map
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		panic("named args source must be a struct or map[string]any")
	}

	structMap := mapper.TypeMap(rv.Type())
	result := make(map[string]any, len(structMap.Index))

	for _, fi := range structMap.Index {
		// fi.Name 就是解析好的 `db` tag (或者默认的小写字段名)
		if fi.Name == "-" || fi.Name == "" {
			continue
		}

		val := reflectx.FieldByIndexesReadOnly(rv, fi.Index)
		if _, hasOmitempty := fi.Options["omitempty"]; hasOmitempty {
			if val.IsZero() {
				continue // 如果带有 omitempty 且当前是零值，立刻剔除！
			}
		}

		result[fi.Name] = val.Interface()
	}

	return result
}

// extractColsVals 从 struct 或 map 中提取列名和对应值
func extractColsVals(data any) (cols []string, vals []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("extract data failed: %v", r)
		}
	}()

	m := toMap(data)
	if len(m) == 0 {
		return nil, nil, errors.New("no columns found or data must be a struct/map")
	}

	// 排序保证生成的 SQL 字符串绝对稳定，利于 DB 预编译缓存
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	vals = make([]any, len(keys))
	for i, k := range keys {
		vals[i] = m[k]
	}

	return keys, vals, nil
}
