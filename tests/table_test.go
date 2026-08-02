package dba_test

import (
	"testing"

	"github.com/kran/dba"
)

func TestExtractCols_Embedded(t *testing.T) {
	type TS struct {
		CreateTs int64 `db:"create_ts"`
	}
	type M struct {
		TS
		ID   int    `db:"id"`
		Name string `db:"name"`
	}
	cols, _, _ := dba.ColumnsAndValues(M{}, true)
	if len(cols) != 3 {
		t.Fatalf("expected 3 cols, got %d: %v", len(cols), cols)
	}
	// 列顺序 = 字段声明序 (TS 嵌入在首, create_ts 在前)
	expect := []string{"create_ts", "id", "name"}
	for i, c := range cols {
		if c != expect[i] {
			t.Errorf("cols[%d] = %q, want %q", i, c, expect[i])
		}
	}
}

func TestIndexBy(t *testing.T) {
	type item struct {
		ID   int
		Name string
	}
	slice := []item{{1, "a"}, {2, "b"}, {3, "c"}}
	m, err := dba.IndexBy(slice, func(v item) int { return v.ID })
	if err != nil {
		t.Fatalf("IndexBy: %v", err)
	}
	if len(m) != 3 {
		t.Fatalf("expected 3, got %d", len(m))
	}
	if m[1].Name != "a" || m[2].Name != "b" || m[3].Name != "c" {
		t.Errorf("wrong values: %+v", m)
	}
}

func TestIndexBy_Duplicate(t *testing.T) {
	type item struct {
		ID   int
		Name string
	}
	slice := []item{{1, "a"}, {1, "b"}}
	_, err := dba.IndexBy(slice, func(v item) int { return v.ID })
	if err == nil {
		t.Fatal("expected error for duplicate key")
	}
}

func TestGroupBy(t *testing.T) {
	type item struct {
		Group string
		Val   int
	}
	slice := []item{{"x", 1}, {"y", 2}, {"x", 3}}
	m := dba.GroupBy(slice, func(v item) string { return v.Group })
	if len(m) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(m))
	}
	if len(m["x"]) != 2 || m["x"][0].Val != 1 || m["x"][1].Val != 3 {
		t.Errorf("group x: %+v", m["x"])
	}
	if len(m["y"]) != 1 || m["y"][0].Val != 2 {
		t.Errorf("group y: %+v", m["y"])
	}
}
