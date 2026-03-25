package sqlo

// execRowsAffected 执行并返回影响行数
func execRowsAffected(q *Sqlo) (int64, error) {
	result, err := q.Exec()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Dao 泛型 DAO，提供基础 CRUD 操作
type Dao[T any] struct {
	q         *Sqlo
	table     string
	quotedTbl string // 预计算的转义表名
	pk        string // 主键列名
	quotedPK  string // 预计算的转义主键
}

// NewDao 创建一个绑定到指定数据源和表的 DAO，主键默认为 "id"
func NewDao[T any](q *Sqlo, table string) *Dao[T] {
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

// PrimaryKey 设置主键列名，返回自身以支持链式调用
func (d *Dao[T]) PrimaryKey(pk string) *Dao[T] {
	clone := d.copy()
	clone.pk = pk
	clone.quotedPK = d.q.quoter(pk)
	return clone
}

func (d *Dao[T]) TableName(table string) *Dao[T] {
	clone := d.copy()
	clone.table = table
	clone.quotedTbl = d.q.quoter(table)
	return clone
}

// WithTx 返回一个使用事务连接的 DAO 副本
func (d *Dao[T]) WithTx(tx *Sqlo) *Dao[T] {
	clone := d.copy()
	clone.q = tx
	return clone
}

// Q 返回底层 Sqlo，用于自定义查询
func (d *Dao[T]) Q() *Sqlo {
	return d.q
}

// Create 插入单条记录并返回自增主键
// PG: INSERT ... RETURNING pk
// 其它: INSERT → LastInsertId
func (d *Dao[T]) Create(data T) (int64, error) {
	driver := d.q.driverName
	if driver == "postgres" || driver == "pgx" || driver == "pq" {
		var pk int64
		_, err := d.q.Insert(d.table, data).Add("RETURNING " + d.quotedPK).Get(&pk)
		return pk, err
	}

	result, err := d.q.Insert(d.table, data).Exec()
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// CreateRaw 插入单条记录，返回 *Sqlo 供用户继续链式操作
func (d *Dao[T]) CreateRaw(data T) *Sqlo {
	return d.q.Insert(d.table, data)
}

// Update 根据条件更新记录，返回影响行数
func (d *Dao[T]) Update(data any, where string, args ...any) (int64, error) {
	return execRowsAffected(d.q.Update(d.table, data, where, args...))
}

// Delete 根据条件删除记录，返回影响行数
func (d *Dao[T]) Delete(where string, args ...any) (int64, error) {
	return execRowsAffected(d.q.Delete(d.table, where, args...))
}

// GetByID 根据主键获取单条记录
func (d *Dao[T]) GetByID(id any) (*T, error) {
	var v T
	found, err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+d.quotedPK+" = #{1}", id).Get(&v)
	if !found || err != nil {
		return nil, err
	}
	return &v, err
}

// Get 根据条件获取单条记录
func (d *Dao[T]) Get(where string, args ...any) (*T, error) {
	var v T
	found, err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).Get(&v)
	if !found || err != nil {
		return nil, err
	}
	return &v, err
}

// List 根据条件获取多条记录
func (d *Dao[T]) List(where string, args ...any) ([]T, error) {
	var list []T
	err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).List(&list)
	return list, err
}

// All 获取全部记录
func (d *Dao[T]) All() ([]T, error) {
	var list []T
	err := d.q.Add("SELECT * FROM " + d.quotedTbl).List(&list)
	return list, err
}

// Count 根据条件计数
func (d *Dao[T]) Count(where string, args ...any) (int64, error) {
	count, _, err := Scalar[int64](d.q.Add("SELECT COUNT(1) FROM "+d.quotedTbl+" WHERE "+where, args...))
	return count, err
}

// CountAll 全表计数
func (d *Dao[T]) CountAll() (int64, error) {
	count, _, err := Scalar[int64](d.q.Add("SELECT COUNT(1) FROM " + d.quotedTbl))
	return count, err
}

// Exists 判断是否存在满足条件的记录
func (d *Dao[T]) Exists(where string, args ...any) (bool, error) {
	count, err := d.Count(where, args...)
	return count > 0, err
}
