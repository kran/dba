package dba_test

import (
	"testing"

	"github.com/kran/dba"
)

func TestDaoVars(t *testing.T) {
	type user struct {
		ID   int    `db:"id"`
		Name string `db:"name"`
	}
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)
	dao := dba.NewDao[user](q, "users")

	m := dao.Vars("u")
	// key 集合: u.as / u / u.pk, 无列引用
	for _, key := range []string{"u.as", "u", "u.pk"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
	if _, ok := m["u.id"]; ok {
		t.Error("column reference should not be generated (列名裸写)")
	}
	if _, ok := m["u.*"]; ok {
		t.Error("star via ${u}.* combination, not a separate key")
	}

	// 展开验证: ${u} 是别名引用, ${u.as} 是表声明, ${u.pk} 是主键
	sql, _, err := q.Vars(m).Add("SELECT ${u}.*, ${u}.name, ${u.pk} FROM ${u.as}").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `u`.*, `u`.name, `u`.`id` FROM `users` AS `u`"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
}

func TestDaoVars_NoAlias(t *testing.T) {
	type user struct {
		ID int `db:"id"`
	}
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)
	dao := dba.NewDao[user](q, "users")
	// 无 alias: Vars 返回 nil, 表名裸写
	if m := dao.Vars(""); m != nil {
		t.Fatalf("Vars without alias should be nil, got %v", m)
	}
}

func TestDaoVars_CustomPK(t *testing.T) {
	type customer struct {
		ID int `db:"id"`
	}
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)
	dao := dba.NewDao[customer](q, "customers").PK("customer_id")
	m := dao.Vars("c")
	sql, _, err := q.Vars(m).Add("SELECT ${c.pk} FROM ${c.as}").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT `c`.`customer_id` FROM `customers` AS `c`" {
		t.Errorf("sql: %q", sql)
	}
}

// JOIN 场景: 星号 + 裸列名 (列引用不再需要 vars)
func TestDaoVarsJoin(t *testing.T) {
	type user struct {
		ID int `db:"id"`
	}
	type profile struct {
		UserID int `db:"user_id"`
	}
	q := dba.NewFromSqlx(newDB(t)).Quoter(dba.MySQLQuoter).Formater(dba.QmarkFormat)
	q = q.Vars(dba.NewDao[user](q, "users").Vars("u"))
	q = q.Vars(dba.NewDao[profile](q, "user_profiles").Vars("p"))

	sql, args, err := q.Add("SELECT ${u}.*, ${p}.email FROM ${u.as} LEFT JOIN ${p.as} ON ${p}.user_id = ${u.pk} WHERE ${u}.name LIKE #{1}", "%t%").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT `u`.*, `p`.email FROM `users` AS `u` LEFT JOIN `user_profiles` AS `p` ON `p`.user_id = `u`.`id` WHERE `u`.name LIKE ?"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 1 || args[0] != "%t%" {
		t.Errorf("args: %v", args)
	}
}
