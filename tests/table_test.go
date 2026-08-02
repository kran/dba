package dba_test

import (
	"testing"

	"github.com/kran/dba"
)

func TestTableBuild(t *testing.T) {
	m := dba.Table("users", "u").Fields("id", "name").Build()

	if n, ok := m["u"]; !ok {
		t.Error(`missing key "u"`)
	} else if n.RawSQL != "@{1} AS @{2}" || len(n.Args) != 2 || n.Args[0] != "users" || n.Args[1] != "u" {
		t.Errorf(`"u" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}

	if n, ok := m["u.id"]; !ok {
		t.Error(`missing key "u.id"`)
	} else if n.RawSQL != "@{1}.@{2}" || n.Args[0] != "u" || n.Args[1] != "id" {
		t.Errorf(`"u.id" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}

	if n, ok := m["u.*"]; !ok {
		t.Error(`missing key "u.*"`)
	} else if n.RawSQL != "@{1}.*" || n.Args[0] != "u" {
		t.Errorf(`"u.*" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}

	if n, ok := m["users"]; !ok {
		t.Error(`missing key "users"`)
	} else if n.RawSQL != "@{1}" || n.Args[0] != "users" {
		t.Errorf(`"users" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}

	if n, ok := m["users.id"]; !ok {
		t.Error(`missing key "users.id"`)
	} else if n.RawSQL != "@{1}.@{2}" || n.Args[0] != "users" || n.Args[1] != "id" {
		t.Errorf(`"users.id" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}

	if n, ok := m["users.*"]; !ok {
		t.Error(`missing key "users.*"`)
	} else if n.RawSQL != "@{1}.*" || n.Args[0] != "users" {
		t.Errorf(`"users.*" = {RawSQL:%q, Args:%v}`, n.RawSQL, n.Args)
	}
}

func TestTableBuild_NoFields(t *testing.T) {
	m := dba.Table("users", "u").Build()
	if _, ok := m["u.*"]; !ok {
		t.Error("expected 'u.*' even without fields — wildcard is always available")
	}
	if _, ok := m["u"]; !ok {
		t.Error("expected 'u' key even without fields")
	}
	if _, ok := m["users"]; !ok {
		t.Error("expected 'users' key even without fields")
	}
}

func TestTableVarsExpansion(t *testing.T) {
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)

	q = q.Vars(dba.Table("customers", "c").Fields("id", "name").Build())
	q = q.Add("SELECT ${c.*} FROM ${c}")

	sql, _, err := q.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}

	want := "SELECT `c`.* FROM `customers` AS `c`"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestTableVarsJoin(t *testing.T) {
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)

	q = q.Vars(dba.Table("users", "u").Fields("id", "name").Build())
	q = q.Vars(dba.Table("profiles", "p").Fields("user_id", "email").Build())

	q = q.Add("SELECT ${u.*}, ${p.email}")
	q = q.Add("FROM ${u} LEFT JOIN ${p} ON ${p.user_id} = ${u.id}")
	q = q.Add("WHERE ${u.name} LIKE #{1}", "%test%")

	sql, args, err := q.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}

	wantSQL := "SELECT `u`.*, `p`.`email`\nFROM `users` AS `u` LEFT JOIN `profiles` AS `p` ON `p`.`user_id` = `u`.`id`\nWHERE `u`.`name` LIKE ?"
	if sql != wantSQL {
		t.Errorf("got  %q\nwant %q", sql, wantSQL)
	}
	if len(args) != 1 || args[0] != "%test%" {
		t.Errorf("args = %v, want [%%test%%]", args)
	}
}

func TestTableStruct(t *testing.T) {
	type model struct {
		ID        int    `db:"id"`
		Name      string `db:"name"`
		Internal  string `db:"-"` // skipped
		NoSkipped string // no db tag → skipped
	}
	m := dba.Table("items", "i").Struct(model{}).Build()

	if _, ok := m["i.id"]; !ok {
		t.Error("missing 'i.id' from Struct")
	}
	if _, ok := m["i.name"]; !ok {
		t.Error("missing 'i.name' from Struct")
	}
	if _, ok := m["i.internal"]; ok {
		t.Error("unexpected 'i.internal' — db:\"-\" should be skipped")
	}
	if _, ok := m["i.noskipped"]; !ok {
		t.Error("unexpected 'i.noskipped' — no db tag should not be skipped")
	}
}

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
