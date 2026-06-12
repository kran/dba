package dba

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx/reflectx"
)

// Scalar returns a single scalar value from a query.
func Scalar[T any](d *SQL) (T, bool, error) {
	var v T
	found, err := d.Get(&v)
	return v, found, err
}

// Page executes a paginated query. Internally substitutes F with COUNT(1)
// for the total count, then adds LIMIT/OFFSET for the data page.
// The query must contain ${F:...} or have Var(F, ...) registered.
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
		if strings.Contains(node.rawSQL, needle) {
			hasF = true
			break
		}
	}

	if !hasF {
		if _, ok := q.varNodes[F]; !ok {
			return nil, 0, fmt.Errorf("dba: page requires ${F:...} or Var(F, ...) in query")
		}
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

// ToMap converts a struct or map to map[string]any using db tags.
// Panics if model is neither a struct nor a map.
func ToMap(model any) map[string]any {
	if m, ok := model.(map[string]any); ok {
		return m
	}

	rv := reflect.ValueOf(model)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return make(map[string]any)
		}
		rv = rv.Elem()
	}

	if rv.Kind() != reflect.Struct {
		panic("named args source must be a struct or map[string]any")
	}

	structMap := mapper.TypeMap(rv.Type())
	result := make(map[string]any, len(structMap.Index))
	valuableType := reflect.TypeOf((*driver.Valuer)(nil)).Elem()

	for _, fi := range structMap.Index {
		if fi.Name == "-" || fi.Name == "" {
			continue
		}

		// skip sub-fields of Valuer types — treat the whole thing as an atomic column
		if len(fi.Index) > 1 {
			parentField := rv.Type().FieldByIndex(fi.Index[:len(fi.Index)-1])
			if parentField.Type.Implements(valuableType) {
				continue
			}
		}

		val := reflectx.FieldByIndexesReadOnly(rv, fi.Index)

		// skip non-Valuer structs — sub-fields are already expanded by reflectx
		if val.Kind() == reflect.Struct && !val.Type().Implements(valuableType) {
			continue
		}

		if _, hasOmitempty := fi.Options["omitempty"]; hasOmitempty {
			v := val
			for v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()
			}
			if v.IsZero() {
				continue
			}
		}

		result[fi.Name] = val.Interface()
	}

	return result
}

// ExtractColsVals extracts sorted column names and values from a struct or map.
func ExtractColsVals(data any) (cols []string, vals []any, err error) {
	m := ToMap(data)
	if len(m) == 0 {
		return nil, nil, errors.New("dba: no columns found or data must be a struct/map")
	}

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
