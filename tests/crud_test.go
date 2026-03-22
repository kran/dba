package stupidql_test

import (
	"testing"
)

type testUser struct {
	ID   int    `db:"id"`
	Name string `db:"name"`
}

func TestInsert_Struct(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", testUser{ID: 1, Name: "alice"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "users" ("id", "name") VALUES (?, ?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != 1 || args[1] != "alice" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_Map(t *testing.T) {
	q, _ := newQ(t)
	// map keys are sorted: age, name
	sql, args, err := q.Insert("users", map[string]any{"name": "alice", "age": 30}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "users" ("age", "name") VALUES (?, ?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != 30 || args[1] != "alice" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_SkipDashTag(t *testing.T) {
	type WithSkip struct {
		Name   string `db:"name"`
		Secret string `db:"-"`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("t", WithSkip{Name: "alice", Secret: "pw"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "t" ("name") VALUES (?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NoTagFallsBackToFieldName(t *testing.T) {
	type NoTag struct {
		Name string
	}
	q, _ := newQ(t)
	sql, _, err := q.Insert("t", NoTag{Name: "alice"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "t" ("Name") VALUES (?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestUpdate_Struct(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", testUser{ID: 1, Name: "bob"}, "WHERE id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"id\"=?, \"name\"=?\nWHERE id = ?"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 || args[0] != 1 || args[1] != "bob" || args[2] != 1 {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Map(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", map[string]any{"name": "bob"}, "WHERE id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"name\"=?\nWHERE id = ?"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != "bob" || args[1] != 5 {
		t.Errorf("args: %v", args)
	}
}

func TestDelete(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Delete("users", "WHERE id = #{1}", 99).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "DELETE FROM \"users\"\nWHERE id = ?"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != 99 {
		t.Errorf("args: %v", args)
	}
}

// TestUpdate_SliceValue 验证切片作为列值（如 PG Array/JSON 列）时不被 sqlx.In 展开
func TestUpdate_SliceValue_NotExpanded(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Update("users", data, "WHERE id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 期望: name=?, tags=?, WHERE id=? → 3 个参数
	// 实际: sqlx.In 把 tags 切片展开 → 4 个参数，SQL 语法报错
	wantSQL := "UPDATE \"users\" SET \"name\"=?, \"tags\"=?\nWHERE id = ?"
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 3 {
		t.Errorf("args: got %d %v, want 3", len(args), args)
	}
}

// TestInsert_SliceValue 同上，Insert 场景
func TestInsert_SliceValue_NotExpanded(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Insert("users", data).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// map 排序后: name, tags → 2 个参数
	wantSQL := `INSERT INTO "users" ("name", "tags") VALUES (?, ?)`
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 2 {
		t.Errorf("args: got %d %v, want 2", len(args), args)
	}
}

func TestInsert_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	_, err = q.Insert("users", testUser{ID: 1, Name: "alice"}).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var name string
	if err := db.QueryRow("SELECT name FROM users WHERE id = 1").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "alice" {
		t.Errorf("got %q", name)
	}
}

func TestUpdate_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO users VALUES (1, 'alice')")

	_, err = q.Update("users", map[string]any{"name": "bob"}, "WHERE id = #{1}", 1).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var name string
	db.QueryRow("SELECT name FROM users WHERE id = 1").Scan(&name)
	if name != "bob" {
		t.Errorf("got %q", name)
	}
}

func TestDelete_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO users VALUES (1, 'alice')")
	db.Exec("INSERT INTO users VALUES (2, 'bob')")

	_, err = q.Delete("users", "WHERE id = #{1}", 1).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}
