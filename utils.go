package sqlo

import (
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx/reflectx"
	"reflect"
	"sort"
	"strings"
)

// Scalar 泛型函数，获取单个标量值（如 COUNT、MAX、单列查询等）
func Scalar[T any](d *Sqlo) (T, bool, error) {
	var v T
	found, err := d.Get(&v)
	return v, found, err
}

// Page 泛型分页查询，要求 q 使用 Mark(F, ...) 标记 SELECT 字段
// 内部用 COUNT(1) 替换 F 查总数，原 query 加 LIMIT/OFFSET 查数据
func Page[T any](q *Sqlo, page, size int) ([]T, int64, error) {
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
	total, _, err := Scalar[int64](q.Mark(F, "COUNT(1)"))
	var items []T
	if err != nil || total == 0 {
		return items, total, err
	}
	// 查数据
	offset := (page - 1) * size
	err = q.Add("LIMIT #{1} OFFSET #{2}", size, offset).List(&items)
	return items, total, err
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

// ToMap 极简版的反射转换为 map
func ToMap(model any) map[string]any {
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

// ExtractColsVals 从 struct 或 map 中提取列名和对应值
func ExtractColsVals(data any) (cols []string, vals []any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("extract data failed: %v", r)
		}
	}()

	m := ToMap(data)
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
