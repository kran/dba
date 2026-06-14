package dba_test

import (
	"github.com/kran/dba"
	"strings"
	"testing"
)

// ==================== 混合宏 ====================

func TestBuild_MixedMacros(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("SELECT @{1} FROM !{2} WHERE id = #{3}", "name", "users", 42).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "name" FROM users WHERE id = $1`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != 42 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_AllMacroTypes(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("SELECT ${F:*} FROM @{1} WHERE NAME = #{2} ORDER BY !{3}", "users", "alice", "id DESC").
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM "users" WHERE NAME = $1 ORDER BY id DESC`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args: %v", args)
	}
}

// ==================== #{} 参数绑定边界 ====================

func TestBuild_SameArgReferencedTwice(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE a = #{1} AND b = #{1}", 99).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE a = $1 AND b = $2"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != 99 || args[1] != 99 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_MultipleAddsIndependentArgs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("WHERE a = #{1}", 10).
		Add("AND b = #{1}", 20).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE a = $1\nAND b = $2"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != 10 || args[1] != 20 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_NamedArgFromMapMultipleFields(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add(
		"WHERE name = #{name} AND age > #{age} AND city = #{city}",
		map[string]any{"name": "alice", "age": 18, "city": "NYC"},
	).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE name = $1 AND age > $2 AND city = $3"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 3 || args[0] != "alice" || args[1] != 18 || args[2] != "NYC" {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_NamedArgFromStructPointer(t *testing.T) {
	type Filter struct {
		Name string `db:"name"`
		Age  int    `db:"age"`
	}
	q, _ := newQ(t)
	f := &Filter{Name: "bob", Age: 25}
	sql, args, err := q.Add("WHERE name = #{name} AND age = #{age}", f).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE name = $1 AND age = $2"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != "bob" || args[1] != 25 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_SliceMultipleInClauses(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("WHERE id IN (#{1})", []int{1, 2}).
		Add("AND name IN (#{1})", []string{"a", "b", "c"}).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE id IN ($1, $2)\nAND name IN ($3, $4, $5)"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 5 {
		t.Errorf("expected 5 args, got %d: %v", len(args), args)
	}
}

func TestBuild_NilArg(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE a = #{1}", nil).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE a = $1"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 1 || args[0] != nil {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_PointerToSlice(t *testing.T) {
	q, _ := newQ(t)
	ids := []int{10, 20}
	sql, args, err := q.Add("WHERE id IN (#{1})", &ids).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE id IN ($1, $2)"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != 10 || args[1] != 20 {
		t.Errorf("args: %v", args)
	}
}

// ==================== ${} 变量宏 ====================

func TestBuild_VarDefault_NotOverridden(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT * ${order:ORDER BY id}").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT * ORDER BY id" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_VarDefault_EmptyString(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT * FROM t${where:}").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT * FROM t" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_VarNotSet_NoDefault(t *testing.T) {
	q, _ := newQ(t)
	// ${key} 没有默认值且未设置 → 什么都不输出
	sql, _, err := q.Add("SELECT * ${missing} FROM t").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT *  FROM t" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_NestedVar(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("SELECT * FROM t ${filter}").
		Var("filter", "WHERE id = #{1} ${extra}", 5).
		Var("extra", "AND active = #{1}", true).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT * FROM t WHERE id = $1 AND active = $2"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != 5 || args[1] != true {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_VarDefaultCannotNestBraces(t *testing.T) {
	q, _ := newQ(t)
	// ${} 的默认值不能包含 } (会被提前截断)，这是已知限制
	// 需要嵌套宏时应使用 Var 而非 inline default
	_, _, err := q.Add("SELECT * ${order:ORDER BY !{name}}").ToSQL()
	if err == nil {
		t.Error("expected error for nested braces in default")
	}
}

func TestBuild_VarDefaultSimpleText(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT * ${order:ORDER BY name DESC}").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT * ORDER BY name DESC" {
		t.Errorf("got %q", sql)
	}
}

// ==================== @{} 标识符转义 ====================

func TestBuild_IdentifierNoArgError(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("SELECT @{USER} FROM t").ToSQL()
	if err == nil {
		t.Error("expected error for @{} with no args")
	}
}

func TestBuild_IdentifierFromNamedArg(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT @{col} FROM t", map[string]any{"col": "email"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != `SELECT "email" FROM t` {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_MultipleIdentifiers(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT @{1}, @{2} FROM @{3}", "id", "name", "users").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "id", "name" FROM "users"`
	if sql != want {
		t.Errorf("got %q", sql)
	}
}

// ==================== !{} 原始输出 ====================

func TestBuild_RawNoArgError(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("ORDER BY !{created_at DESC}").ToSQL()
	if err == nil {
		t.Error("expected error for !{} with no args")
	}
}

func TestBuild_RawFromNamedArg(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("ORDER BY !{sort}", map[string]any{"sort": "name ASC"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "ORDER BY name ASC" {
		t.Errorf("got %q", sql)
	}
}

// ==================== 转义（双写前缀） ====================

func TestBuild_DoubleHashEscape(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add(`WHERE a = ##{1}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE a = #{1}" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestBuild_DoubleHashEscapeMixed(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add(`##{literal} AND id = #{1}`, 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "#{literal} AND id = $1"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 1 || args[0] != 5 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_DoubleHashEscapeTriplePrefix(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add(`###{1}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "##{1}" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestBuild_DoubleDollarEscape(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add(`text $${var}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "text ${var}" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_DoubleAtEscape(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add(`col @@{name}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "col @{name}" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_DoubleBangEscape(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add(`val !!{raw}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "val !{raw}" {
		t.Errorf("got %q", sql)
	}
}

// ==================== 错误场景 ====================

func TestBuild_UnclosedBrace(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id = #{1").ToSQL()
	if err == nil {
		t.Error("expected error for unclosed brace")
	}
}

func TestBuild_ArgIndexZero(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id = #{0}", 1).ToSQL()
	if err == nil {
		t.Error("expected error for index 0")
	}
}

func TestBuild_ArgIndexOutOfRange(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id = #{3}", 1, 2).ToSQL()
	if err == nil {
		t.Error("expected error for index out of range")
	}
}

func TestBuild_NamedArgNoSource(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE name = #{name}").ToSQL()
	if err == nil {
		t.Error("expected error for named arg with no source")
	}
}

func TestBuild_NamedArgMissingKey(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE name = #{missing}", map[string]any{"other": 1}).ToSQL()
	if err == nil {
		t.Error("expected error for missing key in map")
	}
}

func TestBuild_IdentifierNilError(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("SELECT @{1}", nil).ToSQL()
	if err == nil {
		t.Error("expected error for nil identifier arg")
	}
}

func TestBuild_RawNilError(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("ORDER BY !{1}", nil).ToSQL()
	if err == nil {
		t.Error("expected error for nil raw arg")
	}
}

func TestBuild_EmptySliceArg(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id IN (#{1})", []int{}).ToSQL()
	if err == nil {
		t.Error("expected error for empty slice")
	}
}

// ==================== 普通大括号不干扰 ====================

func TestBuild_PlainBracesIgnored(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT JSON_BUILD_OBJECT('a', {1})").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT JSON_BUILD_OBJECT('a', {1})" {
		t.Errorf("got %q", sql)
	}
}

func TestBuild_CurlyInString(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE data = #{1}", `{"key":"val"}`).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "WHERE data = $1"
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 1 || args[0] != `{"key":"val"}` {
		t.Errorf("args: %v", args)
	}
}

// ==================== SQLExpr 通过 Var 统一 ====================

func TestBuild_ExprInInsert_MultipleExprs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("t", map[string]any{
		"a": dba.Expr("NOW()"),
		"b": dba.Expr("#{1} + #{2}", 10, 20),
		"c": "hello",
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// map 排序: a, b, c
	want := `INSERT  INTO "t" ("a", "b", "c") VALUES (NOW(), $1 + $2, $3)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 || args[0] != 10 || args[1] != 20 || args[2] != "hello" {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_ExprInUpdate_MultipleExprs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("t", map[string]any{
		"a": dba.Expr("a + #{1}", 1),
		"b": dba.Expr("COALESCE(b, #{1})", "default"),
		"c": 42,
	}, "id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `UPDATE "t" SET "a"=a + $1, "b"=COALESCE(b, $2), "c"=$3 WHERE` + "\n" + `id = $4`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 4 || args[0] != 1 || args[1] != "default" || args[2] != 42 || args[3] != 5 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_ExprNoArgs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("t", map[string]any{
		"ts": dba.Expr("CURRENT_TIMESTAMP"),
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "t" ("ts") VALUES (CURRENT_TIMESTAMP)`
	if sql != want {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

// ==================== 复杂组合 ====================

func TestBuild_ComplexQuery(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("SELECT ${F:u.id, u.name} FROM @{1} u", "users").
		Add("JOIN orders o ON o.user_id = u.id").
		AddIf(true, "WHERE u.status = #{1}", "active").
		AddIf(false, "AND u.deleted = #{1}", true).
		Add("AND o.amount > #{1}", 100).
		Var("F", "u.id, u.name, COUNT(o.id) AS order_count").
		Add("GROUP BY u.id, u.name").
		Add("ORDER BY !{1}", "order_count DESC").
		Add("LIMIT #{1}", 20).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`SELECT u.id, u.name, COUNT(o.id) AS order_count FROM "users" u`,
		"JOIN orders o ON o.user_id = u.id",
		"WHERE u.status = $1",
		"AND o.amount > $2",
		"GROUP BY u.id, u.name",
		"ORDER BY order_count DESC",
		"LIMIT $3",
	}, "\n")
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 || args[0] != "active" || args[1] != 100 || args[2] != 20 {
		t.Errorf("args: %v", args)
	}
}

func TestBuild_PageStyleQuery(t *testing.T) {
	q, _ := newQ(t)
	// 模拟 Page 的工作方式
	base := q.
		Add("SELECT ${F:*} FROM items").
		Add("WHERE cat = #{1}", "electronics").
		Add("ORDER BY id")

	// count query
	countSQL, countArgs, err := base.Var(dba.F, "COUNT(1)").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	wantCount := "SELECT COUNT(1) FROM items\nWHERE cat = $1\nORDER BY id"
	if countSQL != wantCount {
		t.Errorf("count got  %q\nwant %q", countSQL, wantCount)
	}
	if len(countArgs) != 1 || countArgs[0] != "electronics" {
		t.Errorf("count args: %v", countArgs)
	}

	// data query
	dataSQL, dataArgs, err := base.Add("LIMIT #{1} OFFSET #{2}", 10, 20).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	wantData := "SELECT * FROM items\nWHERE cat = $1\nORDER BY id\nLIMIT $2 OFFSET $3"
	if dataSQL != wantData {
		t.Errorf("data got  %q\nwant %q", dataSQL, wantData)
	}
	if len(dataArgs) != 3 || dataArgs[0] != "electronics" || dataArgs[1] != 10 || dataArgs[2] != 20 {
		t.Errorf("data args: %v", dataArgs)
	}
}

func TestBuild_ImmutableVarDoesNotAffectBase(t *testing.T) {
	q, _ := newQ(t)
	base := q.Add("SELECT ${F:*} FROM t")

	v1 := base.Var(dba.F, "id, name")
	v2 := base.Var(dba.F, "COUNT(1)")

	sql1, _, _ := base.ToSQL()
	sql2, _, _ := v1.ToSQL()
	sql3, _, _ := v2.ToSQL()

	if sql1 != "SELECT * FROM t" {
		t.Errorf("base mutated: %q", sql1)
	}
	if sql2 != "SELECT id, name FROM t" {
		t.Errorf("v1: %q", sql2)
	}
	if sql3 != "SELECT COUNT(1) FROM t" {
		t.Errorf("v2: %q", sql3)
	}
}
