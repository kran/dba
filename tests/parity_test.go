package dba_test

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx/reflectx"
	"github.com/kran/dba"
)

// ── 对拍测试: 自建 walker 的列名集合必须与旧实现 (reflectx TypeMap + 跳过规则) 一致 ──

type parityAliasTime time.Time

type parityBase struct {
	CreateTs int64 `db:"create_ts"`
}

type parityModel struct {
	parityBase
	ID    int64           `db:"id"`
	NS    sql.NullString  `db:"ns"`       // Valuer 值
	NSPtr *sql.NullString `db:"ns_ptr"`   // Valuer 指针
	At    time.Time       `db:"at"`       // time.Time 值
	AtPtr *time.Time      `db:"at_ptr"`   // time.Time 指针
	Alias parityAliasTime `db:"alias_at"` // time.Time 别名
	Inner struct {
		Num int `db:"num"`
	} // 非原子 struct 展开
	time.Time     // 匿名嵌入原子
	Skip      int `db:"-"`
	hidden    int `db:"hidden"` // unexported
}

// legacyColumns 旧实现 (reflectx TypeMap + 原子子条目/非原子 struct 跳过) 的列名模拟。
func legacyColumns(model any) []string {
	rv := reflect.ValueOf(model)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		rv = rv.Elem()
	}
	atomic := func(t reflect.Type) bool {
		return t.ConvertibleTo(reflect.TypeOf(time.Time{})) ||
			t.Implements(reflect.TypeOf((*driver.Valuer)(nil)).Elem())
	}
	m := reflectx.NewMapperFunc("db", strings.ToLower)
	var keys []string
	for _, fi := range m.TypeMap(rv.Type()).Index {
		if fi.Name == "-" || fi.Name == "" || !fi.Field.IsExported() {
			continue
		}
		if len(fi.Index) > 1 {
			parent := rv.Type().FieldByIndex(fi.Index[:len(fi.Index)-1])
			if atomic(parent.Type) {
				continue
			}
		}
		val := reflectx.FieldByIndexesReadOnly(rv, fi.Index)
		if val.Kind() == reflect.Struct && !atomic(val.Type()) {
			continue
		}
		keys = append(keys, fi.Name)
	}
	return keys
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func TestParityColumnSet(t *testing.T) {
	model := parityModel{}
	oldCols := legacyColumns(model)
	newKeys, _, err := dba.ColumnsAndValues(&model, false)
	if err != nil {
		t.Fatal(err)
	}
	oldSorted, newSorted := sortedCopy(oldCols), sortedCopy(newKeys)
	if !reflect.DeepEqual(oldSorted, newSorted) {
		t.Fatalf("column set mismatch:\nold=%v\nnew=%v", oldSorted, newSorted)
	}
}

// 指针非原子 struct: 新行为递归展开子列 (旧行为父条目+子条目并存, 绑定 *struct 会报错)
func TestPtrStructExpands(t *testing.T) {
	type sub struct {
		A int `db:"a"`
	}
	type ptrSubModel struct {
		Sub *sub `db:"sub"`
	}
	keys, _, err := dba.ColumnsAndValues(&ptrSubModel{Sub: &sub{A: 1}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "a" {
		t.Fatalf("ptr struct should expand to child columns, got %v", keys)
	}
}
