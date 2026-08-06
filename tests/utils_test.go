package dba_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kran/dba"
)

// ── ColumnsAndValues 回归: 原子 struct 类型 (time.Time 及别名 / driver.Valuer) 必须整体作为单列 ──

type tkvEmbedded struct {
	CreateTs int64 `db:"create_ts,omitempty"`
}

type tkvModel struct {
	tkvEmbedded
	ID        int64      `db:"id,omitempty"`
	Name      string     `db:"name"`
	CreatedAt time.Time  `db:"created_at,omitempty"` // 值类型 time.Time (曾静默丢失)
	UpdatedAt *time.Time `db:"updated_at,omitempty"` // 指针 time.Time
	Inner     struct {
		At  time.Time `db:"inner_at,omitempty"` // 嵌套 struct 内的 time.Time
		Num int       `db:"inner_num"`
	} // 非匿名 struct, reflectx 展开子字段
}

type tkvAliasTime time.Time

type tkvAliasModel struct {
	At tkvAliasTime `db:"at,omitempty"` // time.Time 别名 (ConvertibleTo 语义)
}

func mustKV(t *testing.T, m any, omitempty bool) ([]string, []any) {
	t.Helper()
	keys, vals, err := dba.ColumnsAndValues(m, omitempty)
	if err != nil {
		t.Fatal(err)
	}
	return keys, vals
}

func findVal(t *testing.T, keys []string, vals []any, want string) any {
	t.Helper()
	for i, k := range keys {
		if k == want {
			return vals[i]
		}
	}
	t.Fatalf("key %q not in %v", want, keys)
	return nil
}

func TestColumnsAndValuesTimeValue(t *testing.T) {
	now := time.Now()
	m := tkvModel{
		tkvEmbedded: tkvEmbedded{CreateTs: 1},
		Name:        "x",
		CreatedAt:   now,
		UpdatedAt:   &now,
	}
	m.Inner.At = now
	m.Inner.Num = 7

	keys, vals := mustKV(t, m, true)

	// 值类型 time.Time 必须整体写入 (回归: 曾静默丢失)
	if v := findVal(t, keys, vals, "created_at"); v.(time.Time) != now {
		t.Fatalf("created_at mismatch: %v", v)
	}
	// 指针 time.Time 解引用后按值绑定 (driver 原生类型)
	if v := findVal(t, keys, vals, "updated_at"); v.(time.Time) != now {
		t.Fatalf("updated_at mismatch: %v", v)
	}
	// 嵌套 struct 内的 time.Time
	if v := findVal(t, keys, vals, "inner_at"); v.(time.Time) != now {
		t.Fatalf("inner_at mismatch: %v", v)
	}
	// 嵌套 struct 普通字段照旧 (不被误吞)
	if v := findVal(t, keys, vals, "inner_num"); v.(int) != 7 {
		t.Fatalf("inner_num mismatch: %v", v)
	}
	// 匿名嵌入照旧
	if v := findVal(t, keys, vals, "create_ts"); v.(int64) != 1 {
		t.Fatalf("create_ts mismatch: %v", v)
	}
	// 无 time.Time 重复项
	count := 0
	for _, k := range keys {
		if k == "created_at" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created_at should appear exactly once, got %d", count)
	}
}

func TestColumnsAndValuesTimeOmitempty(t *testing.T) {
	m := tkvModel{Name: "x"} // CreatedAt/UpdatedAt 零值

	keys, _ := mustKV(t, m, true)
	for _, k := range keys {
		if k == "created_at" || k == "updated_at" || k == "inner_at" {
			t.Fatalf("zero time field %q should be omitted, keys=%v", k, keys)
		}
	}

	// 非 omitempty 模式: 零值也写入
	keys2, _ := mustKV(t, m, false)
	has := false
	for _, k := range keys2 {
		if k == "created_at" {
			has = true
		}
	}
	if !has {
		t.Fatalf("omitempty=false should include zero created_at, keys=%v", keys2)
	}
}

func TestColumnsAndValuesTimeAlias(t *testing.T) {
	now := time.Now()
	keys, vals := mustKV(t, tkvAliasModel{At: tkvAliasTime(now)}, true)
	if len(keys) != 1 || keys[0] != "at" {
		t.Fatalf("alias time should be atomic column, keys=%v", keys)
	}
	// 别名 Convert 为 time.Time (normalizeBindValue — 绑得出去)
	if v, ok := vals[0].(time.Time); !ok || !v.Equal(now) {
		t.Fatalf("at mismatch: %v", vals[0])
	}
	// 零值 + omitempty: 跳过
	keys2, _ := mustKV(t, tkvAliasModel{}, true)
	if len(keys2) != 0 {
		t.Fatalf("zero alias time should be omitted, keys=%v", keys2)
	}
}

// 匿名嵌入 time.Time: reflectx 不展开未导出字段 (wall/ext/loc), 父条目保留, 作为原子列写入
func TestColumnsAndValuesEmbeddedTime(t *testing.T) {
	type embedTime struct {
		time.Time
		X int `db:"x"`
	}
	now := time.Now()
	keys, vals := mustKV(t, embedTime{Time: now, X: 1}, false)
	if len(keys) != 2 {
		t.Fatalf("keys mismatch: %v", keys)
	}
	if v := findVal(t, keys, vals, "time"); v.(time.Time) != now {
		t.Fatalf("embedded time mismatch: %v", v)
	}
	if v := findVal(t, keys, vals, "x"); v.(int) != 1 {
		t.Fatalf("x mismatch: %v", v)
	}
}

func TestColumnsAndValuesPlainStruct(t *testing.T) {
	type plain struct {
		A int    `db:"a"`
		B string `db:"b,omitempty"`
	}
	keys, vals := mustKV(t, plain{A: 1}, true)
	if !reflect.DeepEqual(keys, []string{"a"}) {
		t.Fatalf("keys mismatch: %v", keys)
	}
	if !reflect.DeepEqual(vals, []any{1}) {
		t.Fatalf("vals mismatch: %v", vals)
	}
}

// ── 边角场景补充 ──

type edgePtrBase struct {
	PB int `db:"pb"`
}

type edgeSub struct {
	S int `db:"s"`
}

type edgeModel struct {
	Raw   []byte            `db:"raw"`          // []byte 单列
	JSON  json.RawMessage   `db:"json_payload"` // []byte 别名
	NoTag string            // 无 tag → 列名 "notag"
	M     map[string]string `db:"m"`     // map 单列
	Items []int             `db:"items"` // 切片单列
	Any   any               `db:"any"`   // interface{} 单列
	Deep  struct {
		L2 struct {
			L3 int `db:"l3"`
		} `db:"l2"`
	} `db:"deep"` // 三层嵌套 → 仅产出叶子 l3
	PtrEmbed *edgePtrBase // 指针嵌入 → 展开 pb
	NilSub   *edgeSub     `db:"nil_sub"` // nil 指针 struct → 展开 s=0
	PP       **time.Time  `db:"pp"`      // 指针链
}

func TestColumnsAndValuesEdgeCases(t *testing.T) {
	now := time.Now()
	pp := &now
	m := edgeModel{
		Raw:      []byte("x"),
		JSON:     json.RawMessage(`{"a":1}`),
		NoTag:    "n",
		M:        map[string]string{"k": "v"},
		Items:    []int{1, 2},
		Any:      "iface",
		PtrEmbed: &edgePtrBase{PB: 7},
		PP:       &pp,
	}
	m.Deep.L2.L3 = 3

	keys, vals, err := dba.ColumnsAndValues(&m, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"raw", "json_payload", "notag", "m", "items", "any", "l3", "pb", "s", "pp"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys mismatch:\n got %v\nwant %v", keys, want)
	}
	// nil 指针 struct 展开为子列零值 (FieldByIndexesReadOnly 安全穿越 nil)
	if v := findVal(t, keys, vals, "s"); v.(int) != 0 {
		t.Fatalf("nil sub should expand to zero value, got %v", v)
	}
	// 指针链取值 (二级指针解引用一级 → *time.Time 绑定)
	if v := findVal(t, keys, vals, "pp"); v.(*time.Time) != pp {
		t.Fatalf("pp mismatch: %v", v)
	}
	// 列名集合: 不含中间 struct 列名
	for _, banned := range []string{"deep", "l2", "ptr_embed", "nil_sub"} {
		for _, k := range keys {
			if k == banned {
				t.Fatalf("column %q should not appear, keys=%v", banned, keys)
			}
		}
	}
}

// ── 审计补充: 行为正确但未锁住的边界 ──

// 匿名指针嵌入: *struct 匿名字段递归展开
func TestColumnsAndValuesAnonPtrEmbed(t *testing.T) {
	type anonBase struct {
		B int `db:"b"`
	}
	type anonModel struct {
		*anonBase
		X int `db:"x"`
	}
	keys, vals, err := dba.ColumnsAndValues(&anonModel{anonBase: &anonBase{B: 1}, X: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "b" || keys[1] != "x" {
		t.Fatalf("keys: %v", keys)
	}
	if vals[0] != 1 || vals[1] != 2 {
		t.Fatalf("vals: %v", vals)
	}
}

// nil []byte + omitempty: 省略
func TestColumnsAndValuesNilBytesOmitempty(t *testing.T) {
	type bytesModel struct {
		Raw []byte `db:"raw,omitempty"`
		OK  bool   `db:"ok"`
	}
	keys, vals, err := dba.ColumnsAndValues(bytesModel{OK: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "ok" {
		t.Fatalf("nil []byte should be omitted: %v", keys)
	}
	if vals[0] != true {
		t.Fatalf("vals: %v", vals)
	}
}

// 非法输入: int 报错, nil 报错 (信息含 nil)
func TestColumnsAndValuesInvalidInput(t *testing.T) {
	if _, _, err := dba.ColumnsAndValues(42, true); err == nil {
		t.Fatal("expected error for int input")
	}
	_, _, err := dba.ColumnsAndValues(nil, true)
	if err == nil || !strings.Contains(err.Error(), "got nil") {
		t.Fatalf("expected 'got nil' error, got %v", err)
	}
}

// map 输入: 键排序
func TestColumnsAndValuesMapSorted(t *testing.T) {
	keys, vals, err := dba.ColumnsAndValues(map[string]any{"z": 1, "m": 2, "a": 3}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0] != "a" || keys[1] != "m" || keys[2] != "z" {
		t.Fatalf("keys should be sorted: %v", keys)
	}
	if vals[0] != 3 || vals[1] != 2 || vals[2] != 1 {
		t.Fatalf("vals: %v", vals)
	}
}
