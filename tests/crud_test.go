package stupidql_test

import (
	"testing"
)

type testUser struct {
	ID   int    `db:"id"`
	Name string `db:"name"`
}

type autoUser struct {
	ID   int    `db:"id,omitempty"` // 自增主键，零值时排除
	Name string `db:"name"`
	Age  int    `db:"age,omitempty"` // 可选字段
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
	want := `INSERT INTO "t" ("name") VALUES (?)`
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

// omitempty: 零值字段被过滤，用于自增主键 INSERT 场景
func TestInsert_Omitempty_ZeroID(t *testing.T) {
	q, _ := newQ(t)
	// ID=0 应被过滤，Age=0 也应被过滤
	sql, args, err := q.Insert("users", autoUser{Name: "alice"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "users" ("name") VALUES (?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != "alice" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_Omitempty_NonZeroID(t *testing.T) {
	q, _ := newQ(t)
	// ID 非零时保留
	sql, args, err := q.Insert("users", autoUser{ID: 5, Name: "bob", Age: 18}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT INTO "users" ("age", "id", "name") VALUES (?, ?, ?)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_Omitempty_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, age INTEGER)")
	if err != nil {
		t.Fatal(err)
	}

	// ID=0 被过滤，让数据库自动生成主键
	_, err = q.Insert("users", autoUser{Name: "alice", Age: 20}).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var id int
	var name string
	db.QueryRow("SELECT id, name FROM users LIMIT 1").Scan(&id, &name)
	if id == 0 {
		t.Error("expected auto-generated id, got 0")
	}
	if name != "alice" {
		t.Errorf("got name %q", name)
	}
}

// TestUpdate_SliceExpanded 切片参数会被 sqlx.In 展开，这是预期行为。
// 若要传 PG Array/JSON 等列值，需用 pq.Array、json.RawMessage 等包装类型。
func TestUpdate_SliceExpanded(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Update("users", data, "WHERE id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// tags 切片被展开为两个 ?，共 4 个参数
	wantSQL := "UPDATE \"users\" SET \"name\"=?, \"tags\"=?, ?\nWHERE id = ?"
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 4 {
		t.Errorf("args: got %d %v, want 4", len(args), args)
	}
}

// TestInsert_SliceExpanded 同上，Insert 场景
func TestInsert_SliceExpanded(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Insert("users", data).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// tags 切片被展开，VALUES 从 2 个 ? 变成 3 个
	wantSQL := `INSERT INTO "users" ("name", "tags") VALUES (?, ?, ?)`
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 3 {
		t.Errorf("args: got %d %v, want 3", len(args), args)
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
