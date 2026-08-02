package dba

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx/reflectx"
)

// Map transforms each element of a slice using fn and returns a new slice.
func Map[T any, R any](slice []T, fn func(T) R) []R {
	result := make([]R, len(slice))
	for i, v := range slice {
		result[i] = fn(v)
	}
	return result
}

// IndexBy converts a slice into a map keyed by fn(element).
// Returns an error if duplicate keys are found.
func IndexBy[T any, K comparable](slice []T, fn func(T) K) (map[K]T, error) {
	m := make(map[K]T, len(slice))
	for _, v := range slice {
		key := fn(v)
		if _, ok := m[key]; ok {
			return nil, fmt.Errorf("dba: IndexBy duplicate key %v", key)
		}
		m[key] = v
	}
	return m, nil
}

// GroupBy groups slice elements by fn(element): 1 key → N values.
func GroupBy[T any, K comparable](slice []T, fn func(T) K) map[K][]T {
	m := make(map[K][]T, len(slice))
	for _, v := range slice {
		key := fn(v)
		m[key] = append(m[key], v)
	}
	return m
}

// Scalar returns a single scalar value from a query.
func Scalar[T any](d *SQL) (T, bool, error) {
	var v T
	found, err := d.Get(&v)
	return v, found, err
}

// Page the query must contain ${F:...} so that Page can swap the field list for COUNT(1).
func Page[T any](q *SQL, page, size int) ([]T, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}

	hasF := false
	needle := "${" + F
	for _, node := range q.mainNodes {
		if strings.Contains(node.RawSQL, needle) {
			hasF = true
			break
		}
	}
	if !hasF {
		return nil, 0, fmt.Errorf("dba: page requires ${%s:...} in query", F)
	}

	total, _, err := Scalar[int64](q.Var(F, "COUNT(1)"))
	var items []T
	if err != nil || total == 0 {
		return items, total, err
	}
	offset := (page - 1) * size
	err = q.Add("LIMIT #{1} OFFSET #{2}", size, offset).List(&items)
	return items, total, err
}

// IsOk returns true if v is non-nil, non-blank string, or non-empty
// slice/array/map.
func IsOk(v any) bool {
	if v == nil {
		return false
	}

	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val) != ""
	}

	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.String:
		return strings.TrimSpace(rv.String()) != ""
	case reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() > 0
	case reflect.Invalid:
		return false
	default:
		return true
	}
}

func ToKeyValue(model any, omitempty bool) ([]string, []any, error) {
	rv := reflect.ValueOf(model)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return []string{}, []any{}, nil
		}
		rv = rv.Elem()
	}

	// ── Map branch ──────────────────────────────────────────
	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, nil, fmt.Errorf("dba: ToKV map key must be string, got %s", rv.Type().Key().Kind())
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return keys[i].String() < keys[j].String()
		})
		result := make([]string, len(keys))
		vals := make([]any, len(keys))
		for i, k := range keys {
			result[i] = k.String()
			vals[i] = rv.MapIndex(k).Interface()
		}
		return result, vals, nil
	}

	// ── Struct branch ───────────────────────────────────────
	if rv.Kind() != reflect.Struct {
		return nil, nil, fmt.Errorf("dba: ToKV expects struct or map[string]any, got %s", rv.Kind())
	}

	structMap := mapper.TypeMap(rv.Type())
	keys := make([]string, 0, len(structMap.Index))
	vals := make([]any, 0, len(structMap.Index))

	for _, fi := range structMap.Index {
		if fi.Name == "-" || fi.Name == "" {
			continue
		}

		// skip unexported fields
		if !fi.Field.IsExported() {
			continue
		}

		// 原子 struct 类型 (driver.Valuer 实现者如 sql.NullString, 以及 database/sql
		// 原生参数类型 time.Time) 的子字段由 reflectx 展开, 必须跳过 — 父字段会作为
		// 整体原子列由本循环的单级条目写入。time.Time 不实现 Valuer, 需显式特判,
		// 否则值类型时间字段会在 INSERT/UPDATE 中静默丢失。
		if len(fi.Index) > 1 {
			parentField := rv.Type().FieldByIndex(fi.Index[:len(fi.Index)-1])
			if isAtomicColumn(parentField.Type) {
				continue
			}
		}

		val := reflectx.FieldByIndexesReadOnly(rv, fi.Index)

		// skip 非原子 struct — 子字段已由 reflectx 展开 (递归 case)
		if val.Kind() == reflect.Struct && !isAtomicColumn(val.Type()) {
			continue
		}

		// omitempty: skip zero-valued fields
		if _, hasOmitempty := fi.Options["omitempty"]; hasOmitempty && omitempty {
			if isZeroValue(val) {
				continue
			}
		}

		keys = append(keys, fi.Name)
		vals = append(vals, val.Interface())
	}

	return keys, vals, nil
}

// timeType time.Time 是 database/sql 原生参数类型 (不实现 driver.Valuer)。
var timeType = reflect.TypeOf(time.Time{})

// valuableType driver.Valuer 接口类型。
var valuableType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// isAtomicColumn 判断 struct 类型是否应整体作为单列写入:
//  1. 实现 driver.Valuer (sql.NullString/NullInt64/自定义类型)
//  2. time.Time 及其可转换别名 — database/sql 原生参数类型, 不实现 Valuer;
//     用 ConvertibleTo 而非 ==, 覆盖 type MyTime time.Time 这类别名 (对齐 GORM schema.ParseField)
func isAtomicColumn(t reflect.Type) bool {
	return t.ConvertibleTo(timeType) || t.Implements(valuableType)
}

// valuerAncestorDepth 沿展开链从近到远找第一个实现 driver.Valuer 的祖先字段深度

// isZeroValue 指针解引用后的零值判断 (omitempty 语义)。
func isZeroValue(v reflect.Value) bool {
	for v.Kind() == reflect.Ptr && !v.IsNil() {
		v = v.Elem()
	}
	return v.IsZero()
}
