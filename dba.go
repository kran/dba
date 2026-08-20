package dba

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

// F is the default field variable name used by Select and Page.
// F 是 Select/Page 的列集槽位变量名。
//
// 契约:
//   - Select 生成 "${F:*}" (默认 *), Page 用 Var(F, "COUNT(1)") 换计数。
//   - 手写 SQL 接入 Page: 主链必须含 ${F:...} 或裸 ${F} (见 Page 的探测)。
//   - 约定 F 槽在主链 (mainNodes); 藏在 varNode 里的 ${F} 不被 Page 识别。
const F = "F"

// I 是 INSERT 修饰词槽位 (INSERT ${I:} INTO ...), 默认为空。
// 存在理由: Insert 生成后, 链式 Add 只能追加尾部 (ON DUPLICATE KEY /
// RETURNING / ON CONFLICT 天然可达), 唯独 INSERT 与 INTO 之间是死角,
// 此槽是唯一通气孔:
//
//	db.Var(I, "IGNORE").Insert("users", u)  // INSERT IGNORE INTO ...
const I = "I"

// O 是排序槽位变量名。Page 的 count 查询会 Var(O, "") 清空它,
// 因此接入 Page 的查询应把 ORDER BY 写成 ${O:...} 而非裸文本:
//
//	q.Add("SELECT ... WHERE x ${O:ORDER BY id DESC}")
const O = "order"

// H is a shorthand for map[string]any.
type H = map[string]any

// Expr creates a Node fragment; passed as an argument it is inlined into
// the SQL (参数即子树) instead of bound as a placeholder.
func Expr(sql string, args ...any) Node {
	return Node{Text: sql, Args: args}
}

// LogFunc SQL 执行日志回调: 每次执行后调用一次 (begin 为执行开始时间,
// 观察者自行计算耗时/开 span; 不改变执行流, panic 不影响执行结果)。
type LogFunc func(ctx context.Context, begin time.Time, query string, args []any, err error)

// execFunc 实际执行函数 (内部): 调用底层 sqlx/database 完成查询。
type execFunc func(ctx context.Context, query string, args []any) (any, error)

// Formatter generates a placeholder for the n-th parameter.
type Formatter func(idx int) string

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
	Text string
	Args []any
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
	formatter  Formatter
	driverName string
	logger     LogFunc // 执行日志回调 (nil = 静默)
}

// NewFromSqlx creates a SQL builder from a sqlx.DB. Auto-detects driver for
// placeholder formatter and identifier quoting.
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
		formatter:  format,
		driverName: driver,
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
		executor:   d.executor,
		pool:       d.pool,
		ctx:        d.ctx,
		err:        d.err,
		quoter:     d.quoter,
		formatter:  d.formatter,
		driverName: d.driverName,
	}
	copy(clone.mainNodes, d.mainNodes)
	// 低频写字段: 共享只读, 写操作 (Var/Vars/Use/Register*) 各自 copy-on-write
	clone.varNodes = d.varNodes
	clone.pipes = d.pipes
	clone.macros = d.macros
	clone.logger = d.logger
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

// Formatter returns a new builder with the given placeholder formatter.
func (d *SQL) Formatter(formatter Formatter) *SQL {
	clone := d.copy()
	clone.formatter = formatter
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

// SetLogger 设置执行日志回调 (nil 关闭)。copy-on-write。
func (d *SQL) SetLogger(fn LogFunc) *SQL {
	clone := d.copy()
	clone.logger = fn
	return clone
}

func (d *SQL) execute(fn execFunc) (any, error) {
	query, args, err := d.build()
	if err != nil {
		return nil, err
	}
	begin := time.Now()
	result, err := fn(d.ctx, query, args)
	if d.logger != nil {
		d.logger(d.ctx, begin, query, args, err)
	}
	return result, err
}

// Add appends a SQL fragment and returns a new builder.
func (d *SQL) Add(query string, args ...any) *SQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.mainNodes = append(clone.mainNodes, Node{Text: query, Args: args})
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
	clone.varNodes[key] = Node{Text: query, Args: args}
	return clone
}

func (d *SQL) Vars(vars map[string]Node) *SQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}
	clone.varNodes = maps.Clone(clone.varNodes) // copy-on-write
	maps.Copy(clone.varNodes, vars)
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
			// Node 值也作为普通参数: bind 管道内联 (参数即子树)
			builder.WriteString(fmt.Sprintf("#{%d}", argIdx))
			bindArgs = append(bindArgs, val)
			argIdx++
		}
		builder.WriteString(")")
	}

	return d.Add(builder.String(), bindArgs...)
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

// FetchList scans multiple rows and returns them as a slice (struct/basic
// types, sqlx mapping). For dynamic queries with unknown columns use FetchMaps.
func (d *SQL) FetchList[T any]() ([]T, error) {
	var list []T
	_, err := d.execute(func(ctx context.Context, query string, args []any) (any, error) {
		return nil, sqlx.SelectContext(ctx, d.executor, &list, query, args...)
	})
	return list, err
}

// FetchMaps scans multiple rows into a slice of maps — for queries whose
// columns are not known at compile time.
func (d *SQL) FetchMaps() ([]map[string]any, error) {
	rows, err := d.FetchRows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []map[string]any
	for rows.Next() {
		m := make(map[string]any)
		if err := rows.MapScan(m); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}

// FetchOne scans a single row and returns it by value.
// 严格 0..1: 无行 → (零值, false, nil); 多于一行 → 报错 (多取一行检测,
// 抓住漏写 LIMIT 1 的 bug)。需要“随便取一行”时在 SQL 里显式写 LIMIT 1。
func (d *SQL) FetchOne[T any]() (T, bool, error) {
	rows, err := d.FetchRows()
	if err != nil {
		var zero T
		return zero, false, err
	}
	defer rows.Close()
	return fetchOneRows[T](rows)
}

// fetchOneRows 严格 0..1 的单行扫描 (FetchOne 主体)。
func fetchOneRows[T any](rows *sqlx.Rows) (T, bool, error) {
	var v T
	if !rows.Next() {
		var zero T
		if err := rows.Err(); err != nil {
			return zero, false, err
		}
		return zero, false, nil
	}
	if err := scanRow(rows, &v); err != nil {
		var zero T
		return zero, false, err
	}
	if rows.Next() {
		var zero T
		return zero, false, fmt.Errorf("dba: fetch one: query returned more than one row")
	}
	return v, true, rows.Err()
}

// Iter returns a lazy iterator over the rows (struct/basic types, sqlx
// mapping). 惰性: 查询在 for-range 开始时才执行; break 提前退出会正确释放
// 连接。每行需判 err (iter.Seq2[T, error] 的固有形态)。
//
//	for u, err := range q.Iter[User]() {
//	    if err != nil {
//	        return err
//	    }
//	    // ...
//	}
func (d *SQL) Iter[T any]() iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		rows, err := d.FetchRows()
		if err != nil {
			var zero T
			yield(zero, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var v T
			if err := scanRow(rows, &v); err != nil {
				var zero T
				yield(zero, err)
				return
			}
			if !yield(v, nil) {
				return // break: 连接由 defer 释放
			}
		}
		if err := rows.Err(); err != nil {
			var zero T
			yield(zero, err)
		}
	}
}

// sqlScannerType database/sql.Scanner 的反射类型。
var sqlScannerType = reflect.TypeFor[sql.Scanner]()

// scanRow 按 FetchList/FetchOne 同一套分流逻辑扫描单行:
// 标量 / Scanner 实现者 / 无导出映射字段的 struct (如 time.Time) → Scan;
// 其余 struct → StructScan (sqlx 映射)。
//
// 指针类型 T (如 *User) 与 FetchList 行为对齐: 全层级解引用后再判定
// (reflectx.Deref 只解一层, 直接照搬会把 *User 误判为标量), struct 路径先
// 分配内层指针再 StructScan, 保证 FetchOne[*User]/Iter[*User] 可用。
func scanRow(rows *sqlx.Rows, dest any) error {
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return errors.New("dba: scan: internal dest must be a non-nil pointer")
	}

	elem := v.Type().Elem()
	base := elem
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}

	if isScannableForScan(base) {
		return rows.Scan(dest) // 标量指针链由 driver 的 convertAssign 分配
	}

	if elem.Kind() == reflect.Pointer {
		// dest 是 **T (T 为 struct): 分配内层指针并写回调用方变量,
		// StructScan 收到 *T (指向新分配内存, 与 sqlx 扫 []*T 同机制)
		inner := reflect.New(base)
		v.Elem().Set(inner)
		dest = inner.Interface()
	}
	return rows.StructScan(dest)
}

// isScannableForScan 镜像 sqlx 的 isScannable 判定 (与 FetchList/FetchOne 行为一致):
//
//  1. *T 或 T 实现 sql.Scanner (指针方法集覆盖值接收者)
//  2. 非 struct 类型 (int/string/[]byte/...)
//  3. struct 但无导出映射字段 (time.Time: wall/ext/loc 均未导出)
//
// 注: 此处用包级 mapper (与 sqlx 默认 mapper 同规则) 数导出映射字段;
// 与 sqlx 自身的 isScannable 相同, 只关心“导出字段数”这一粗粒度事实。
func isScannableForScan(t reflect.Type) bool {
	if reflect.PointerTo(t).Implements(sqlScannerType) {
		return true
	}
	if t.Kind() != reflect.Struct {
		return true
	}
	return len(mapper.TypeMap(t).Index) == 0
}

// FetchOneMap scans a single row into a map — for queries whose columns are
// not known at compile time. 与 FetchOne 同契约: 严格 0..1, 多于一行报错。
// found=false (nil map) when no row matches.
func (d *SQL) FetchOneMap() (map[string]any, bool, error) {
	rows, err := d.FetchRows()
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	m := make(map[string]any)
	if err := rows.MapScan(m); err != nil {
		return nil, false, err
	}
	if rows.Next() {
		return nil, false, fmt.Errorf("dba: fetch one map: query returned more than one row")
	}
	return m, true, nil
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

// FetchRows returns a raw *sqlx.Rows cursor (查询立即执行, 适合流式场景;
// 惰性流式请用 Iter)。
func (d *SQL) FetchRows() (*sqlx.Rows, error) {
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

	for i, c := range cols {
		quotedCols[i] = d.quoter(c)
		// Node 值也作为普通参数: bind 管道内联 (参数即子树)
		placeholders[i] = fmt.Sprintf("#{%d}", i+1)
	}
	query := fmt.Sprintf("INSERT ${"+I+":} INTO %s (%s) VALUES (%s)",
		d.quoter(table),
		strings.Join(quotedCols, ", "),
		strings.Join(placeholders, ", "),
	)
	return d.Add(query, vals...)
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
	for i, c := range cols {
		// Node 值也作为普通参数: bind 管道内联 (参数即子树)
		setClauses[i] = d.quoter(c) + "=" + fmt.Sprintf("#{%d}", i+1)
	}
	setQuery := fmt.Sprintf("UPDATE %s SET %s WHERE",
		d.quoter(table),
		strings.Join(setClauses, ", "),
	)
	return d.Add(setQuery, vals...).Add(where, args...)
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

// maxRenderDepth 渲染递归深度上限: 防 ${a} 自引用 / Node 嵌套循环导致的栈溢出。
const maxRenderDepth = 64

func (d *SQL) build() (string, []any, error) {
	if d.err != nil {
		return "", nil, d.err
	}

	w := &buildRenderCtx{d: d}
	w.sb.Grow(512)

	for i, node := range d.mainNodes {
		if i > 0 {
			w.sb.WriteString("\n")
		}
		if err := d.renderText(w, node.Text, node.Args); err != nil {
			return "", nil, err
		}
	}

	return w.sb.String(), w.out, nil
}

// renderText 渲染一段模板文本 (宏展开 + 参数收集)。
// 递归入口 ($ 变量/Node 内联) 共用: 深度护栏 + 参数作用域隔离。
func (d *SQL) renderText(w *buildRenderCtx, text string, args []any) error {
	w.depth++
	defer func() { w.depth-- }()
	if w.depth > maxRenderDepth {
		return errors.New("dba: render depth exceeded (possible cycle)")
	}

	items, err := lex(text, d.macros)
	if err != nil {
		return err
	}

	prev := w.args
	w.args = args                    // 当前参数列表 (var 递归时 = varNode.Args)
	defer func() { w.args = prev }() // 递归返回后恢复, 外层宏继续用外层参数

	for _, it := range items {
		switch it.kind {
		case itemText:
			w.sb.WriteString(it.val)

		case itemMacro:
			if err := d.renderMacro(w, it); err != nil {
				return err
			}
		default:
			// itemError 已被 lex 过滤, 此处兜底未来扩展的 token 类型
			return fmt.Errorf("dba: unexpected token kind %d", it.kind)
		}
	}
	return nil
}

// renderMacro 渲染宏 token: '#' = 管道容器, '$' = 变量 (结构层), 其他 = 宏别名。
func (d *SQL) renderMacro(bw *buildRenderCtx, it item) error {
	// $ 变量: 结构层 (模板递归), 不经管道
	if it.prefix == '$' {
		parts := strings.SplitN(it.val, ":", 2)
		key := strings.TrimSpace(parts[0])
		if varNode, ok := d.varNodes[key]; ok {
			return d.renderText(bw, varNode.Text, varNode.Args)
		}
		if len(parts) == 2 {
			return d.renderText(bw, strings.TrimSpace(parts[1]), nil)
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
	if before, after, ok := strings.Cut(content, "|"); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after), true
	}
	return strings.TrimSpace(content), "", false // 无管道: renderMacro 用宏默认
}
