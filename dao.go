package dba

import (
	"context"
	"errors"
	"fmt"
)

// BeforeCreate is implemented by structs that need a hook before INSERT.
type BeforeCreate interface {
	BeforeCreate() error
}

// BeforeUpdate is implemented by structs that need a hook before UPDATE.
type BeforeUpdate interface {
	BeforeUpdate() error
}

// Dao is a generic single-table CRUD helper.
type Dao[T any] struct {
	q         *SQL
	table     string
	quotedTbl string
	pk        string
	quotedPK  string
}

// NewDao creates a Dao bound to the given table. Default primary key is "id".
func NewDao[T any](q *SQL, table string) *Dao[T] {
	return &Dao[T]{
		q:         q,
		table:     table,
		quotedTbl: q.quoter(table),
		pk:        "id",
		quotedPK:  q.quoter("id"),
	}
}

func (d *Dao[T]) copy() *Dao[T] {
	return &Dao[T]{
		q:         d.q,
		table:     d.table,
		quotedTbl: d.quotedTbl,
		pk:        d.pk,
		quotedPK:  d.quotedPK,
	}
}

// Vars 生成表别名引用变量 (表名/主键在 DAO 一处维护):
//
//	${u.as}  → ` + "`users` AS `u`" + `   (FROM/JOIN 表声明)
//	${u}     → ` + "`u`" + `              (别名引用: ${u}.name / ${u}.*)
//	${u.pk}  → ` + "`u`.`id`" + `          (主键引用: 主键列名 DAO.PK 维护)
//
// 列引用不生成 (列名裸写: `${u}.email`), 列集不属于 DAO 的维护职责。
// 无 alias (单表场景) 返回 nil, 表名裸写即可。
func (d *Dao[T]) Vars(alias string) map[string]Node {
	if alias == "" {
		return nil
	}
	m := make(map[string]Node, 3)
	qa := d.q.quoter(alias)
	m[alias+".as"] = Node{Text: d.quotedTbl + " AS " + qa}
	m[alias] = Node{Text: qa}
	m[alias+".pk"] = Node{Text: qa + "." + d.quotedPK}
	return m
}

func (d *Dao[T]) WithCtx(ctx context.Context) *Dao[T] {
	clone := d.copy()
	clone.q = clone.q.WithCtx(ctx)
	return clone
}

// PK returns a new Dao with the given primary key column.
func (d *Dao[T]) PK(pk string) *Dao[T] {
	clone := d.copy()
	clone.pk = pk
	clone.quotedPK = d.q.quoter(pk)
	return clone
}

// Table returns a new Dao with the given table name.
func (d *Dao[T]) Table(table string) *Dao[T] {
	clone := d.copy()
	clone.table = table
	clone.quotedTbl = d.q.quoter(table)
	return clone
}

// WithTx returns a Dao backed by the given transaction.
func (d *Dao[T]) WithTx(tx *SQL) *Dao[T] {
	clone := d.copy()
	clone.q = tx
	return clone
}

// SQL returns the underlying SQL builder for custom queries.
func (d *Dao[T]) SQL() *SQL {
	return d.q
}

// Create inserts a single record and returns the generated primary key.
// On PostgreSQL, uses RETURNING. On other drivers, uses LastInsertId.
func (d *Dao[T]) Create(data any) (int64, error) {
	if h, ok := data.(BeforeCreate); ok {
		if err := h.BeforeCreate(); err != nil {
			return 0, err
		}
	}

	driver := d.q.driverName
	if driver == "postgres" || driver == "pgx" || driver == "pq" {
		pk, _, err := d.q.Insert(d.table, data).Add("RETURNING " + d.quotedPK).FetchOne[int64]()
		return pk, err
	}

	result, err := d.q.Insert(d.table, data).Exec()
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// RawCreate inserts a single record and returns the SQL builder for chaining
// (e.g. ON CONFLICT, RETURNING).
func (d *Dao[T]) RawCreate(data any) *SQL {
	if h, ok := data.(BeforeCreate); ok {
		if err := h.BeforeCreate(); err != nil {
			clone := d.q.copy()
			clone.err = err
			return clone
		}
	}
	return d.q.Insert(d.table, data)
}

func (d *Dao[T]) RawSelect(where string, args ...any) *SQL {
	return d.q.Select(d.table, where, args...)
}

func (d *Dao[T]) FetchPage(page, size int, where string, args ...any) ([]T, int64, error) {
	return d.RawSelect(where, args...).FetchPage[T](page, size)
}

// RawBatch bulk-inserts multiple records and returns a SQL builder for chaining.
func (d *Dao[T]) RawBatch(entities []T) *SQL {
	if len(entities) == 0 {
		clone := d.q.copy()
		clone.err = errors.New("dba: dao batch create: empty entities")
		return clone
	}

	processed := make([]any, len(entities))
	for i := range entities {
		if h, ok := any(&entities[i]).(BeforeCreate); ok {
			if err := h.BeforeCreate(); err != nil {
				clone := d.q.copy()
				clone.err = fmt.Errorf("dba: dao batch create: entity %d hook error: %w", i, err)
				return clone
			}
		}
		processed[i] = entities[i]
	}

	return d.q.BatchInsert(d.table, processed)
}

// Batch bulk-inserts multiple records and returns affected rows.
func (d *Dao[T]) Batch(entities []T) (int64, error) {
	return execRowsAffected(d.RawBatch(entities))
}

// Update updates records matching the given condition.
func (d *Dao[T]) Update(data any, where string, args ...any) (int64, error) {
	if h, ok := data.(BeforeUpdate); ok {
		if err := h.BeforeUpdate(); err != nil {
			return 0, err
		}
	}

	return execRowsAffected(d.q.Update(d.table, data, where, args...))
}

// Delete deletes records matching the given condition.
func (d *Dao[T]) Delete(where string, args ...any) (int64, error) {
	return execRowsAffected(d.q.Delete(d.table, where, args...))
}

// GetByID fetches a single record by primary key.
// 主键唯一, 内部走严格 FetchOne (>1 行即数据异常, 会报错)。
// 未找到时返回 (零值, false, nil) —— “无结果”是正常业务结果而非错误
// (不返回 sql.ErrNoRows); 调用方必须先判 ok 再使用返回的实体。
func (d *Dao[T]) GetByID(id any) (T, bool, error) {
	return d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+d.quotedPK+" = #{1}", id).FetchOne[T]()
}

// FetchOne fetches a single record by condition (严格 0..1: 多于一行报错)。
// 未找到时返回 (零值, false, nil) —— “无结果”是正常业务结果而非错误
// (不返回 sql.ErrNoRows); 调用方必须先判 ok 再使用返回的实体。
// 条件不保证唯一时请用 FetchList。
func (d *Dao[T]) FetchOne(where string, args ...any) (T, bool, error) {
	return d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).FetchOne[T]()
}

// FetchList fetches multiple records by condition.
func (d *Dao[T]) FetchList(where string, args ...any) ([]T, error) {
	return d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).FetchList[T]()
}

// FetchAll fetches all records from the table.
func (d *Dao[T]) FetchAll() ([]T, error) {
	return d.q.Add("SELECT * FROM " + d.quotedTbl).FetchList[T]()
}

// Count returns the number of matching records.
func (d *Dao[T]) Count(where string, args ...any) (int64, error) {
	count, _, err := d.q.Add("SELECT COUNT(1) FROM "+d.quotedTbl+" WHERE "+where, args...).FetchOne[int64]()
	return count, err
}

// CountAll returns the total number of records in the table.
func (d *Dao[T]) CountAll() (int64, error) {
	count, _, err := d.q.Add("SELECT COUNT(1) FROM " + d.quotedTbl).FetchOne[int64]()
	return count, err
}

// Exists returns true if at least one matching record exists.
func (d *Dao[T]) Exists(where string, args ...any) (bool, error) {
	count, err := d.Count(where, args...)
	return count > 0, err
}

func execRowsAffected(q *SQL) (int64, error) {
	result, err := q.Exec()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
