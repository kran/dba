package dba

import (
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

// PrimaryKey returns a new Dao with the given primary key column.
func (d *Dao[T]) PrimaryKey(pk string) *Dao[T] {
	clone := d.copy()
	clone.pk = pk
	clone.quotedPK = d.q.quoter(pk)
	return clone
}

// TableName returns a new Dao with the given table name.
func (d *Dao[T]) TableName(table string) *Dao[T] {
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

// Q returns the underlying SQL builder for custom queries.
func (d *Dao[T]) Q() *SQL {
	return d.q
}

// Create inserts a single record and returns the generated primary key.
// On PostgreSQL, uses RETURNING. On other drivers, uses LastInsertId.
func (d *Dao[T]) Create(data any) (int64, error) {
	if p := d.resolve(data); p != nil {
		if h, ok := any(p).(BeforeCreate); ok {
			if err := h.BeforeCreate(); err != nil {
				return 0, err
			}
		}
		data = *p
	}

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

// CreateRaw inserts a single record and returns the SQL builder for chaining
// (e.g. ON CONFLICT, RETURNING).
func (d *Dao[T]) CreateRaw(data any) *SQL {
	if p := d.resolve(data); p != nil {
		if h, ok := any(p).(BeforeCreate); ok {
			if err := h.BeforeCreate(); err != nil {
				clone := d.q.copy()
				clone.err = err
				return clone
			}
		}
		data = *p
	}
	return d.q.Insert(d.table, data)
}

// BatchRaw bulk-inserts multiple records and returns a SQL builder for chaining.
func (d *Dao[T]) BatchRaw(entities []T) *SQL {
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
	return execRowsAffected(d.BatchRaw(entities))
}

// Update updates records matching the given condition.
func (d *Dao[T]) Update(data any, where string, args ...any) (int64, error) {
	if p := d.resolve(data); p != nil {
		if h, ok := any(p).(BeforeUpdate); ok {
			if err := h.BeforeUpdate(); err != nil {
				return 0, err
			}
		}
		data = *p
	}

	return execRowsAffected(d.q.Update(d.table, data, where, args...))
}

// Delete deletes records matching the given condition.
func (d *Dao[T]) Delete(where string, args ...any) (int64, error) {
	return execRowsAffected(d.q.Delete(d.table, where, args...))
}

// GetByID fetches a single record by primary key.
func (d *Dao[T]) GetByID(id any) (*T, error) {
	var v T
	found, err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+d.quotedPK+" = #{1}", id).Get(&v)
	if !found || err != nil {
		return nil, err
	}
	return &v, err
}

// Get fetches a single record by condition.
func (d *Dao[T]) Get(where string, args ...any) (*T, error) {
	var v T
	found, err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).Get(&v)
	if !found || err != nil {
		return nil, err
	}
	return &v, err
}

// List fetches multiple records by condition.
func (d *Dao[T]) List(where string, args ...any) ([]T, error) {
	var list []T
	err := d.q.Add("SELECT * FROM "+d.quotedTbl+" WHERE "+where, args...).List(&list)
	return list, err
}

// All fetches all records from the table.
func (d *Dao[T]) All() ([]T, error) {
	var list []T
	err := d.q.Add("SELECT * FROM " + d.quotedTbl).List(&list)
	return list, err
}

// Count returns the number of matching records.
func (d *Dao[T]) Count(where string, args ...any) (int64, error) {
	count, _, err := Scalar[int64](d.q.Add("SELECT COUNT(1) FROM "+d.quotedTbl+" WHERE "+where, args...))
	return count, err
}

// CountAll returns the total number of records in the table.
func (d *Dao[T]) CountAll() (int64, error) {
	count, _, err := Scalar[int64](d.q.Add("SELECT COUNT(1) FROM " + d.quotedTbl))
	return count, err
}

// Exists returns true if at least one matching record exists.
func (d *Dao[T]) Exists(where string, args ...any) (bool, error) {
	count, err := d.Count(where, args...)
	return count > 0, err
}

func (d *Dao[T]) resolve(data any) *T {
	switch v := data.(type) {
	case T:
		return &v
	case *T:
		return v
	default:
		return nil
	}
}

func execRowsAffected(q *SQL) (int64, error) {
	result, err := q.Exec()
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
