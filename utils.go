package dba

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx/reflectx"
)

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
		if strings.Contains(node.RawSQL, needle) {
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

	valuableType := reflect.TypeOf((*driver.Valuer)(nil)).Elem()
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

		// omitempty: skip zero-valued fields
		if _, hasOmitempty := fi.Options["omitempty"]; hasOmitempty && omitempty {
			v := val
			for v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()
			}
			if v.IsZero() {
				continue
			}
		}

		keys = append(keys, fi.Name)
		vals = append(vals, val.Interface())
	}

	return keys, vals, nil
}
