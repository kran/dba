package dba

import (
	"database/sql/driver"
	"fmt"
	"github.com/jmoiron/sqlx/reflectx"
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
// bool is false when the query returned no row.
func Scalar[T any](d *SQL) (T, bool, error) {
	var v T
	found, err := d.Get(&v)
	return v, found, err
}

// Page fetches a page of rows and the total count.
//
// Contract: the query must contain the ${F:...} slot (or bare ${F}) on the
// main chain — the F slot is substituted with COUNT(1) for the count query.
// Constraints: no GROUP BY / DISTINCT in the F slot content (the count query
// reuses the same template).
//
// ORDER BY should live in the ${O:...} slot (the count query clears it with
// Var(O, "") — a bare ORDER BY still works but the count query pays the
// sort). Example:
//
//	q.Add("SELECT ... WHERE x ${O:ORDER BY id DESC}") // then Page(q, 1, 20)
//
// The total==0 case skips the data query entirely.
func Page[T any](q *SQL, page, size int) ([]T, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}

	// F 槽必须在主链 (${F:...} 或裸 ${F}): 探测两种形态, 避免 ${From} 误报
	hasF := false
	for _, node := range q.mainNodes {
		if strings.Contains(node.Text, "${"+F+":") || strings.Contains(node.Text, "${"+F+"}") {
			hasF = true
			break
		}
	}
	if !hasF {
		return nil, 0, fmt.Errorf("dba: page requires ${%s:...} in query (main chain)", F)
	}

	// count 查询: F → COUNT(1), 并清空排序变量 (count 不需要排序, 白付)
	total, _, err := Scalar[int64](q.Var(F, "COUNT(1)").Var(O, ""))
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

// ColumnsAndValues converts a struct or map into column names and bind values.
//
// Struct strategy: fieldList (derived from the reflectx TypeMap — the same
// mapper used by #{name} resolution and row scanning) builds the field list,
// with dba's atomicity decision (isAtomicColumn) applied per field:
//   - atomic types (driver.Valuer implementers / time.Time and convertible
//     aliases / Node) collapse to a single column;
//   - structs (embedded or plain, value or pointer) expand recursively;
//   - other basic types / []byte are single columns.
//
// Collected field values pass through normalizeBindValue before entering vals.
// The map branch does no normalization (same level as Add arguments: bind
// exactly what the caller passed).
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
		// omitempty 判断先于归一化: 指针只看 nilness (逃生舱语义)
		if omitempty && f.omitempty && isZeroValue(val) {
			continue
		}
		keys = append(keys, f.key)
		vals = append(vals, normalizeBindValue(val).Interface())
	}
	return keys, vals, nil
}

// normalizeBindValue 归一化 struct 字段的绑定值 (omitempty 判断之后调用):
//
//  1. 非 nil 指针解引用一级 — 但指针自身实现 Valuer 的除外 (指针接收者的
//     Value 方法只在 *T 上, 解引用会剥掉它, 导致 driver 报 unsupported)。
//     nil 指针原样保留 (Bind 的 *Node nil case / driver 按 NULL 处理)。
//     注: 主流 driver 的 DefaultParameterConverter 本也会解指针; 这里自行
//     解引用是为了 *Node → Node 直达 Bind 内联, 并且不依赖各 driver
//     converter 行为一致 (自定义 NamedValueChecker 的 driver 可能不同)。
//
//  2. time.Time 的可转换别名 (type MyTime time.Time, 自身无 Valuer) 转换为
//     time.Time — isAtomicColumn 按 ConvertibleTo 判它为单列, 但别名类型
//     本身不是 driver 原生类型也无 Valuer, 不转换绑不进去。
//     time.Time 本尊、Node、Valuer 实现者均不转换 (各有自己的绑定路径)。
func normalizeBindValue(val reflect.Value) reflect.Value {
	if val.Kind() == reflect.Ptr {
		if val.IsNil() || val.Type().Implements(valuableType) {
			return val
		}
		val = val.Elem()
	}
	t := val.Type()
	if t.Kind() == reflect.Struct && t != timeType && t != nodeType &&
		!t.Implements(valuableType) && t.ConvertibleTo(timeType) {
		val = val.Convert(timeType)
	}
	return val
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

// fieldList 从 reflectx TypeMap 派生写入列清单。
//
// 命名/嵌入/跳过规则全部由 mapper (与 #{name} 解析、List/Get 扫描共用同一
// 实例) 单点定义: db tag 名优先且原样使用, 无 tag 用 mapFunc(ToLower),
// db:"-" 排除, unexported 跳过 (匿名嵌入类型除外)。
//
// 在 reflectx 展开树上叠加 dba 的原子判定 (isAtomicColumn):
// Valuer / time.Time 及可转换别名 / Node 收束为单列, 不下钻子字段;
// 其余 struct 字段 (值/指针/匿名嵌入) 递归展开, 列名取子字段自身的
// 映射名 fi.Name — 不用 fi.Path 的 "a.b" 点路径 (那是扫描语义,
// INSERT 列名没有前缀概念), 这是与 reflectx 默认展开的唯一分歧点。
//
// 与旧自建遍历的行为差异 (有意为之): 显式 db tag 名不再 ToLower —
// 此前 INSERT 列名强制小写, 而 #{name} 解析和行扫描按 reflectx 原样,
// 同一 tag 两套规则; 现在三处收敛为一套。
func fieldList(t reflect.Type) []kvField {
	if v, ok := fieldListCache.Load(t); ok {
		return v.([]kvField)
	}
	tm := mapper.TypeMap(t) // reflectx 内部按类型缓存, 与扫描共享
	var out []kvField
	var walk func(fis []*reflectx.FieldInfo)
	walk = func(fis []*reflectx.FieldInfo) {
		for _, fi := range fis {
			if fi == nil {
				// Children 按字段序号占位: 被跳过的字段
				// (unexported / db:"-") 留 nil 洞
				continue
			}
			ft := fi.Field.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && !isAtomicColumn(ft) {
				walk(fi.Children) // 非原子 struct: 展开子字段
				continue
			}
			_, omit := fi.Options["omitempty"] // reflectx 已解析 tag 选项
			out = append(out, kvField{
				key:       fi.Name,  // 叶子映射名 (不是 fi.Path)
				path:      fi.Index, // 根起始的完整索引路径
				omitempty: omit,
			})
		}
	}
	walk(tm.Tree.Children)
	fieldListCache.Store(t, out)
	return out
}

// timeType time.Time 是 database/sql 原生参数类型 (不实现 driver.Valuer)。
var timeType = reflect.TypeOf(time.Time{})

// valuableType driver.Valuer 接口类型。
var valuableType = reflect.TypeOf((*driver.Valuer)(nil)).Elem()

// nodeType Node 的反射类型。
var nodeType = reflect.TypeOf(Node{})

// isAtomicColumn 判断 struct 类型是否应整体作为单列写入:
//  1. 实现 driver.Valuer (sql.NullString/NullInt64/自定义类型)
//  2. time.Time 及其可转换别名 — database/sql 原生参数类型, 不实现 Valuer;
//     用 ConvertibleTo 而非 ==, 覆盖 type MyTime time.Time 这类别名 (对齐 GORM schema.ParseField)
//  3. Node — "参数即子树"的值, 整体收束后原样流到 Bind 内联 (不展开为
//     text/args 两个子列)
func isAtomicColumn(t reflect.Type) bool {
	return t == nodeType || t.ConvertibleTo(timeType) || t.Implements(valuableType)
}

// isZeroValue omitempty 语义的零值判断。
// 指针只判 nilness 不解引用: nil = 未设置 (跳过), 非 nil = 显式赋值
// (即使指向零值也保留) — 这是 omitempty 字段写入零值的逃生舱,
// 与 encoding/json 的 omitempty 约定一致。
func isZeroValue(v reflect.Value) bool {
	if v.Kind() == reflect.Ptr {
		return v.IsNil()
	}
	return v.IsZero()
}
