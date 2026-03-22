package stupidql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/jmoiron/sqlx"
)

var (
	formatPhPattern = regexp.MustCompile(`([#@!]?)\{([^}]+?)\}`)
	intPattern      = regexp.MustCompile(`^\d+$`)
)

// Quoter 用于 @{} 宏的标识符安全转义 (比如把 user 转为 `user` 或 "user")
type Quoter func(string) string

func MySQLQuoter(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }
func AnsiQuoter(s string) string  { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// StupidQL 是一个不可变的动态 SQL 拼装引擎
type StupidQL struct {
	queries []string
	args    [][]any
	marks   map[string]int

	db     sqlx.ExtContext // sqlx 的核心接口，兼容 *sqlx.DB 和 *sqlx.Tx
	rawDB  *sqlx.DB        // 仅用于开启事务，进入事务后为 nil
	ctx    context.Context
	err    error
	quoter Quoter
}

// NewStupidQL 包装现有的 sqlx.DB
func NewStupidQL(db *sqlx.DB) *StupidQL {
	quoter := AnsiQuoter // 默认使用 PG 引号
	if db.DriverName() == "mysql" {
		quoter = MySQLQuoter
	}
	return &StupidQL{
		queries: make([]string, 0),
		args:    make([][]any, 0),
		marks:   make(map[string]int),
		db:      db,
		rawDB:   db,
		ctx:     context.Background(),
		quoter:  quoter,
	}
}

// copy 实现不可变模式 (Immutable)
func (d *StupidQL) copy() *StupidQL {
	clone := &StupidQL{
		marks:   make(map[string]int),
		queries: make([]string, len(d.queries)),
		args:    make([][]any, len(d.args)),
		db:      d.db,
		rawDB:   d.rawDB,
		ctx:     d.ctx,
		err:     d.err,
		quoter:  d.quoter,
	}
	copy(clone.queries, d.queries)
	copy(clone.args, d.args)
	for k, v := range d.marks {
		clone.marks[k] = v
	}
	return clone
}

// WithContext 设置上下文
func (d *StupidQL) WithContext(ctx context.Context) *StupidQL {
	clone := d.copy()
	clone.ctx = ctx
	return clone
}

// Add 核心方法：添加 SQL 片段并解析宏
func (d *StupidQL) Add(query string, args ...any) *StupidQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
	}

	defer func() {
		if r := recover(); r != nil {
			clone.err = fmt.Errorf("stupidql add panic: %v", r)
		}
	}()

	parsedQuery, parsedArgs := clone.parseText(query, args...)
	clone.queries = append(clone.queries, parsedQuery)
	clone.args = append(clone.args, parsedArgs)
	return clone
}

// AddIf 条件拼接
func (d *StupidQL) AddIf(cond bool, query string, args ...any) *StupidQL {
	if cond {
		return d.Add(query, args...)
	}
	return d
}

// Mark 预留或替换命名占位符
func (d *StupidQL) Mark(name string, query string, args ...any) *StupidQL {
	clone := d.copy()
	if clone.err != nil {
		return clone
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
	return clone
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

	// 2. [魔法时刻] 交给 sqlx 处理切片展开 (解决 WHERE id IN (?) 的痛点)
	query, flatArgs, err := sqlx.In(query, flatArgs...)
	if err != nil {
		return "", nil, fmt.Errorf("sqlx.In expand failed: %w", err)
	}

	// 3. [魔法时刻] 交给 sqlx 处理方言占位符 (自动把 ? 转为 $1, $2 或者保留 ?)
	query = d.db.Rebind(query)

	return query, flatArgs, nil
}

// ==========================================
// 委托给 sqlx 执行的终端方法
// ==========================================

// Select 映射多行到 Slice
func (d *StupidQL) Select(dest interface{}) error {
	query, args, err := d.build()
	if err != nil {
		return err
	}
	return sqlx.SelectContext(d.ctx, d.db, dest, query, args...)
}

// Get 映射单行到 Struct
func (d *StupidQL) Get(dest interface{}) error {
	query, args, err := d.build()
	if err != nil {
		return err
	}
	return sqlx.GetContext(d.ctx, d.db, dest, query, args...)
}

// Exec 执行非查询 SQL
func (d *StupidQL) Exec() (sql.Result, error) {
	query, args, err := d.build()
	if err != nil {
		return nil, err
	}
	return d.db.ExecContext(d.ctx, query, args...)
}

// Rows 返回原始 *sqlx.Rows，用于流式处理大结果集
func (d *StupidQL) Rows() (*sqlx.Rows, error) {
	query, args, err := d.build()
	if err != nil {
		return nil, err
	}
	return d.db.QueryxContext(d.ctx, query, args...)
}

// ToSQL 返回最终 SQL 和参数，不执行，用于调试和日志
func (d *StupidQL) ToSQL() (string, []any, error) {
	return d.build()
}

// Error 返回 builder 累积的错误
func (d *StupidQL) Error() error {
	return d.err
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

// Transaction 闭包事务：fn 返回 error 或发生 panic 时自动回滚
func (d *StupidQL) Transaction(fn func(*StupidQL) error) error {
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

// ==========================================
// 宏解析引擎 (保留你最核心的资产)
// ==========================================

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
		case "#", "": // 安全的参数绑定
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
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		panic("named args source must be a struct or map[string]any")
	}

	result := make(map[string]any)
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		// 这里可以根据需求扩展读取 db tag
		result[rt.Field(i).Name] = rv.Field(i).Interface()
	}
	return result
}
