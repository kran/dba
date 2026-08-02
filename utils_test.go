package dba

import (
	"reflect"
	"testing"
	"time"
)

// ── ToKeyValue 回归: Valuer 类型字段 (time.Time) 必须整体作为原子列 ──

type tkvEmbedded struct {
	CreateTs int64 `db:"create_ts,omitempty"`
}

type tkvModel struct {
	tkvEmbedded
	ID        int64      `db:"id,omitempty"`
	Name      string     `db:"name"`
	CreatedAt time.Time  `db:"created_at,omitempty"` // 值类型 time.Time (曾静默丢失)
	UpdatedAt *time.Time `db:"updated_at,omitempty"` // 指针 time.Time (一直正常)
	Inner     struct {
		At  time.Time `db:"inner_at,omitempty"` // 嵌套 struct 内的 time.Time
		Num int       `db:"inner_num"`
	} // 非匿名 struct, reflectx 展开子字段
}

func mustKV(t *testing.T, m any, omitempty bool) ([]string, []any) {
	t.Helper()
	keys, vals, err := ToKeyValue(m, omitempty)
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

func TestToKeyValueTimeValue(t *testing.T) {
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
	// 指针 time.Time 照旧
	if v := findVal(t, keys, vals, "updated_at"); v.(*time.Time) != &now {
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
	// 无 time.Time 重复项 (原子化只写一次)
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

func TestToKeyValueTimeOmitempty(t *testing.T) {
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

func TestToKeyValuePlainStruct(t *testing.T) {
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

// time.Time 别名类型 (type MyTime time.Time) 同样必须作为原子列 (ConvertibleTo 语义)
func TestToKeyValueTimeAlias(t *testing.T) {
	type MyTime time.Time
	type aliasModel struct {
		At MyTime `db:"at,omitempty"`
	}
	now := time.Now()
	keys, vals := mustKV(t, aliasModel{At: MyTime(now)}, true)
	if len(keys) != 1 || keys[0] != "at" {
		t.Fatalf("alias time should be atomic column, keys=%v", keys)
	}
	if v, ok := vals[0].(MyTime); !ok || time.Time(v) != now {
		t.Fatalf("at mismatch: %v", vals[0])
	}
	// 零值 + omitempty: 跳过
	keys2, _ := mustKV(t, aliasModel{}, true)
	if len(keys2) != 0 {
		t.Fatalf("zero alias time should be omitted, keys=%v", keys2)
	}
}
