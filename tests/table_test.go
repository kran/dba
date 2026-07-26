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
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Format(dba.QmarkFormat)

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
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Format(dba.QmarkFormat)

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
