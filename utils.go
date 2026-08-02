package dba

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
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

// ColumnsAndValues 把 struct 或 map 转换为列名与参数值列表。
//
// struct 策略: 自建递归遍历 (fieldList) 生成字段清单, 遍历时即时判定:
//   - 原子类型 (driver.Valuer 实现者 / time.Time 及其可转换别名) 作为单列收束;
//   - struct (匿名嵌入或普通字段, 值或指针) 递归展开子字段;
//   - 其余基本类型/[]byte 直接作为单列。
//
// time.Time 不实现 Valuer (database/sql 原生参数类型), 由 isAtomicColumn 显式特判。
func ColumnsAndValues(model any, omitempty bool) ([]string, []any, error) {
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
		if !rv.IsValid() {
			return nil, nil, fmt.Errorf("dba: ToKV expects struct or map[string]any, got nil")
		}
		return nil, nil, fmt.Errorf("dba: ToKV expects struct or map[string]any, got %s", rv.Kind())
	}

	fields := fieldList(rv.Type())
	keys := make([]string, 0, len(fields))
	vals := make([]any, 0, len(fields))
	for _, f := range fields {
		val := fieldByPath(rv, f.path)
		if omitempty && f.omitempty && isZeroValue(val) {
			continue
		}
		keys = append(keys, f.key)
		vals = append(vals, val.Interface())
	}
	return keys, vals, nil
}

// fieldByPath 沿索引路径取值, 自动穿越指针/接口中间层。
// 与 reflectx.FieldByIndexesReadOnly 的区别: nil 指针中间层返回目标字段类型的零值,
// 不 panic (展开 nil *struct 字段时必需)。
func fieldByPath(rv reflect.Value, path []int) reflect.Value {
	// 先沿类型推导目标字段类型 (穿越指针/接口)
	t := rv.Type()
	for _, i := range path {
		for t.Kind() == reflect.Ptr || t.Kind() == reflect.Interface {
			t = t.Elem()
		}
		t = t.Field(i).Type
	}
	// 再沿值取字段 (nil 指针/接口 → 返回目标零值)
	cur := rv
	for _, i := range path {
		for cur.Kind() == reflect.Ptr || cur.Kind() == reflect.Interface {
			if cur.IsNil() {
				return reflect.Zero(t)
			}
			cur = cur.Elem()
		}
		cur = cur.Field(i)
	}
	return cur
}

// kvField 一个待写入列: 列名 + 取值索引路径 + omitempty 选项。
type kvField struct {
	key       string
	path      []int
	omitempty bool
}

var fieldListCache sync.Map // reflect.Type → []kvField

// fieldList 自建递归遍历生成字段清单, 按类型缓存。
// 列名规则与 reflectx TypeMap 展开兼容: db tag 名优先 (忽略 tag 选项),
// 否则字段名, 统一小写 (对齐 mapper.NewMapperFunc("db", strings.ToLower))。
func fieldList(t reflect.Type) []kvField {
	if v, ok := fieldListCache.Load(t); ok {
		return v.([]kvField)
	}
	var out []kvField
	var walk func(t reflect.Type, prefix []int)
	walk = func(t reflect.Type, prefix []int) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			// 对齐 reflectx: unexported 的匿名字段仍可展开 (嵌入类型可为小写), 普通 unexported 跳过
			if (!f.IsExported() && !f.Anonymous) || f.Tag.Get("db") == "-" {
				continue
			}
			path := append(append([]int{}, prefix...), i)
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() != reflect.Struct || isAtomicColumn(ft) {
				// 基本类型 / []byte / 原子类型 (Valuer、time.Time 及别名): 单列
				out = append(out, kvField{
					key:       columnName(f),
					path:      path,
					omitempty: hasOmitempty(f),
				})
				continue
			}
			// 非原子 struct (值或指针): 递归展开子字段
			walk(ft, path)
		}
	}
	walk(t, nil)
	fieldListCache.Store(t, out)
	return out
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

// isZeroValue 指针解引用后的零值判断 (omitempty 语义)。
func isZeroValue(v reflect.Value) bool {
	for v.Kind() == reflect.Ptr && !v.IsNil() {
		v = v.Elem()
	}
	return v.IsZero()
}

// columnName 取字段的 db 列名: db tag 优先 (忽略选项), 否则字段名; 统一小写 (与 mapper 一致)。
func columnName(f reflect.StructField) string {
	if tag := f.Tag.Get("db"); tag != "" {
		return strings.ToLower(strings.Split(tag, ",")[0])
	}
	return strings.ToLower(f.Name)
}

// hasOmitempty 判断 db tag 是否携带 omitempty 选项。
func hasOmitempty(f reflect.StructField) bool {
	for _, o := range strings.Split(f.Tag.Get("db"), ",")[1:] {
		if o == "omitempty" {
			return true
		}
	}
	return false
}
