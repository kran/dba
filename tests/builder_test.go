package dba_test

import (
	"testing"
)

func TestAdd_NoMacro(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("SELECT 1").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT 1" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestAdd_MultiFragment(t *testing.T) {
	q, _ := newQ(t)
	sql, _, err := q.Add("SELECT *").Add("FROM users").Add("LIMIT 10").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT *\nFROM users\nLIMIT 10"
	if sql != want {
		t.Errorf("got %q\nwant %q", sql, want)
	}
}

func TestAdd_PositionalMacro(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE id = #{1} AND age > #{2}", 42, 18).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE id = $1 AND age > $2" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != 42 || args[1] != 18 {
		t.Errorf("expected [42 18], got %v", args)
	}
}

func TestAdd_NamedMacro_Map(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE name = #{Name} AND age > #{Age}", map[string]any{
		"Name": "alice",
		"Age":  18,
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE name = $1 AND age > $2" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != "alice" || args[1] != 18 {
		t.Errorf("expected [alice 18], got %v", args)
	}
}

func TestAdd_NamedMacro_Struct(t *testing.T) {
	type Filter struct {
		Name string
		Age  int
	}
	q, _ := newQ(t)
	sql, args, err := q.Add("WHERE name = #{name} AND age > #{age}", Filter{"bob", 20}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE name = $1 AND age > $2" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 2 || args[0] != "bob" || args[1] != 20 {
		t.Errorf("expected [bob 20], got %v", args)
	}
}

func TestAdd_IdentifierMacro(t *testing.T) {
	q, _ := newQ(t) // sqlite → AnsiQuoter
	sql, _, err := q.Add("SELECT @{1} FROM @{2}", "name", "users").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT "name" FROM "users"`
	if sql != want {
		t.Errorf("got %q\nwant %q", sql, want)
	}
}

func TestAdd_RawMacro(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("ORDER BY !{1}", "created_at DESC").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "ORDER BY created_at DESC" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestAdd_InSlice(t *testing.T) {
	q, _ := newQ(t)
	ids := []int{1, 2, 3}
	sql, args, err := q.Add("WHERE id IN (#{1})", ids).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE id IN ($1, $2, $3)" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 3 {
		t.Errorf("expected 3 args, got %v", args)
	}
}

func TestAddIf_True(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("SELECT 1").AddIf(true, "WHERE id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT 1\nWHERE id = $1" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 1 || args[0] != 5 {
		t.Errorf("expected [5], got %v", args)
	}
}

func TestAddIf_False(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Add("SELECT 1").AddIf(false, "WHERE id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT 1" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 0 {
		t.Errorf("expected no args, got %v", args)
	}
}

func TestVar_Override(t *testing.T) {
	q, _ := newQ(t)
	base := q.Add("SELECT * ${order:ORDER BY id}")
	overridden := base.Var("order", "ORDER BY name")

	sql1, _, _ := base.ToSQL()
	sql2, _, _ := overridden.ToSQL()

	if sql1 != "SELECT * ORDER BY id" {
		t.Errorf("base got %q", sql1)
	}
	if sql2 != "SELECT * ORDER BY name" {
		t.Errorf("overridden got %q", sql2)
	}
}

func TestVar_WithArgs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.
		Add("SELECT * ${where}", "ignored").
		Var("where", "WHERE id = #{1}", 99).
		ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT * WHERE id = $1" {
		t.Errorf("got %q", sql)
	}
	if len(args) != 1 || args[0] != 99 {
		t.Errorf("expected [99], got %v", args)
	}
}

func TestImmutability(t *testing.T) {
	q, _ := newQ(t)
	base := q.Add("SELECT *")
	_ = base.Add("FROM users")

	sql, _, _ := base.ToSQL()
	if sql != "SELECT *" {
		t.Errorf("base was mutated: got %q", sql)
	}
}

func TestError_MissingPositionalArg(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id = #{1}").ToSQL() // no args
	if err == nil {
		t.Error("expected error for missing positional arg")
	}
}

func TestError_MissingNamedArg(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("WHERE id = #{missing}", map[string]any{"other": 1}).ToSQL()
	if err == nil {
		t.Error("expected error for missing named arg")
	}
}

func TestError_Propagates(t *testing.T) {
	q, _ := newQ(t)
	// error from build should surface
	_, _, err := q.Add("WHERE id = #{1}").Add("AND name = #{1}", "alice").ToSQL()
	if err == nil {
		t.Error("expected error to propagate")
	}
}
