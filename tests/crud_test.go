package dba_test

import (
	gosql "database/sql"
	"encoding/json"
	"reflect"

	"github.com/kran/dba"
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
	want := `INSERT  INTO "users" ("id", "name") VALUES ($1, $2)`
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
	want := `INSERT  INTO "users" ("age", "name") VALUES ($1, $2)`
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
	want := `INSERT  INTO "t" ("name") VALUES ($1)`
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
	want := `INSERT  INTO "t" ("name") VALUES ($1)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
}

func TestUpdate_Struct(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", testUser{ID: 1, Name: "bob"}, "id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"id\"=$1, \"name\"=$2 WHERE\nid = $3"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 || args[0] != 1 || args[1] != "bob" || args[2] != 1 {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Map(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"name\"=$1 WHERE\nid = $2"
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != "bob" || args[1] != 5 {
		t.Errorf("args: %v", args)
	}
}

func TestDelete(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Delete("users", "id = #{1}", 99).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "DELETE FROM \"users\" WHERE\nid = $1"
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
	want := `INSERT  INTO "users" ("name") VALUES ($1)`
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
	want := `INSERT  INTO "users" ("id", "name", "age") VALUES ($1, $2, $3)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_Omitempty_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, name text, age INTEGER)")
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
	db.QueryRow("SELECT id, name FROM users limit 1").Scan(&id, &name)
	if id == 0 {
		t.Error("expected auto-generated id, got 0")
	}
	if name != "alice" {
		t.Errorf("got name %q", name)
	}
}

// gosql.Null* + omitempty 行为
type nullableUser struct {
	ID    int              `db:"id,omitempty"`
	Name  gosql.NullString `db:"name,omitempty"`
	Bio   gosql.NullString `db:"bio,omitempty"`
	Age   gosql.NullInt64  `db:"age,omitempty"`
	Score gosql.NullInt64  `db:"score"`
}

func TestInsert_NullString_ValidFalse_Omits(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nullableUser{
		Name: gosql.NullString{Valid: false},
		Age:  gosql.NullInt64{Valid: false},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("score") VALUES ($1)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}

	tv := gosql.NullInt64{}
	if len(args) != 1 || args[0] != tv {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullString_ValidFalseWithNonZeroValue_Keeps(t *testing.T) {
	// Valid=false 但 String 非零值 → IsZero()=false → 保留
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nullableUser{
		Name: gosql.NullString{Valid: false, String: "hello"},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "score") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	ns := gosql.NullString{Valid: false, String: "hello"}
	zeroNI := gosql.NullInt64{}
	if len(args) != 2 || args[0] != ns || args[1] != zeroNI {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullString_ValidTrueWithEmpty_Keeps(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nullableUser{
		Name: gosql.NullString{Valid: true, String: ""},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "score") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}

	emptyStr := gosql.NullString{Valid: true, String: ""}
	zeroNI := gosql.NullInt64{}
	if len(args) != 2 || args[0] != emptyStr || args[1] != zeroNI {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullString_ValidTrueWithValue_Keeps(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nullableUser{
		Name: gosql.NullString{Valid: true, String: "alice"},
		Bio:  gosql.NullString{Valid: true, String: "hello"},
		Age:  gosql.NullInt64{Valid: true, Int64: 25},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "bio", "age", "score") VALUES ($1, $2, $3, $4)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}

	age25 := gosql.NullInt64{Int64: 25, Valid: true}
	bioHello := gosql.NullString{String: "hello", Valid: true}
	nameAlice := gosql.NullString{String: "alice", Valid: true}
	zeroNI := gosql.NullInt64{}
	if len(args) != 4 || args[0] != nameAlice || args[1] != bioHello || args[2] != age25 || args[3] != zeroNI {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullInt64_NoOmitempty_AlwaysKeeps(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nullableUser{
		Name:  gosql.NullString{Valid: true, String: "bob"},
		Score: gosql.NullInt64{Valid: false},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "score") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}

	nameBob := gosql.NullString{String: "bob", Valid: true}
	zeroNI := gosql.NullInt64{}
	if len(args) != 2 || args[0] != nameBob || args[1] != zeroNI {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullablePointer_NilOmits(t *testing.T) {
	type ptrUser struct {
		Name *gosql.NullString `db:"name,omitempty"`
		Age  int               `db:"age"`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", ptrUser{Age: 30}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("age") VALUES ($1)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != 30 {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullablePointer_ValidFalseOmits(t *testing.T) {
	type ptrUser struct {
		Name *gosql.NullString `db:"name,omitempty"`
		Age  int               `db:"age"`
	}
	name := gosql.NullString{Valid: false}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", ptrUser{Name: &name, Age: 30}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("age") VALUES ($1)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != 30 {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NullablePointer_ValidTrueKeeps(t *testing.T) {
	type ptrUser struct {
		Name *gosql.NullString `db:"name,omitempty"`
		Age  int               `db:"age"`
	}
	name := gosql.NullString{Valid: true, String: "carol"}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", ptrUser{Name: &name, Age: 30}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "age") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}

	if len(args) != 2 || args[1] != 30 {
		t.Errorf("args: %v", args)
	}
	if ptr, ok := args[0].(*gosql.NullString); !ok || *ptr != (gosql.NullString{String: "carol", Valid: true}) {
		t.Errorf("args[0]: %v", args[0])
	}
}

// 嵌套 struct：匿名嵌入应展开，非 Valuer 命名 struct 应展开
type BaseEmbed struct {
	CreatedAt string `db:"created_at"`
}

type nestedUser struct {
	BaseEmbed
	Name string `db:"name"`
}

func TestInsert_EmbeddedStruct_Flattens(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", nestedUser{
		Name: "alice",
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 列顺序 = 字段声明序 (嵌入 struct 按声明位置展开)
	want := `INSERT  INTO "users" ("created_at", "name") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != "" || args[1] != "alice" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_EmbeddedStruct_WithNullString(t *testing.T) {
	type embedNullableUser struct {
		BaseEmbed
		Name gosql.NullString `db:"name,omitempty"`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", embedNullableUser{
		Name: gosql.NullString{Valid: true, String: "bob"},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 列顺序 = 字段声明序 (嵌入 struct 按声明位置展开)
	want := `INSERT  INTO "users" ("created_at", "name") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	nameBob := gosql.NullString{String: "bob", Valid: true}
	if len(args) != 2 || args[0] != "" || args[1] != nameBob {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_EmbeddedStruct_NullStringOmitted(t *testing.T) {
	type embedOmitUser struct {
		BaseEmbed
		Name gosql.NullString `db:"name,omitempty"`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", embedOmitUser{
		BaseEmbed: BaseEmbed{CreatedAt: "2024"},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("created_at") VALUES ($1)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 1 || args[0] != "2024" {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_NonValuerNamedStruct_Flattens(t *testing.T) {
	type Meta struct {
		Version int `db:"version"`
	}
	type userWithMeta struct {
		Name string `db:"name"`
		Meta Meta   `db:""`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", userWithMeta{
		Name: "alice",
		Meta: Meta{Version: 3},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("name", "version") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || args[0] != "alice" || args[1] != 3 {
		t.Errorf("args: %v", args)
	}
}

// SQLExpr: 原始 SQL 表达式
func TestUpdate_Expr_NoArgs(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", map[string]any{
		"version": dba.Expr("version+1"),
		"name":    "alice",
	}, "id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// map 排序: name, version
	want := "UPDATE \"users\" SET \"name\"=$1, \"version\"=version+1 WHERE\nid = $2"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	// version+1 没有绑定参数，只有 name 和 WHERE id
	if len(args) != 2 || args[0] != "alice" || args[1] != 1 {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Expr_WithMacro(t *testing.T) {
	q, _ := newQ(t)
	// SQLExpr 内使用 #{} 宏而不是原始 ?
	sql, args, err := q.Update("users", map[string]any{
		"score": dba.Expr("score+#{1}+#{2}", 10, 11),
		"name":  "bob",
	}, "id = #{1}", 2).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"name\"=$1, \"score\"=score+$2+$3 WHERE\nid = $4"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 4 || args[0] != "bob" || args[1] != 10 || args[2] != 11 || args[3] != 2 {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Expr_MultiAdd(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Update("users", map[string]any{
		"score": dba.Expr("score+#{1}+#{2}", 10, 11),
		"age":   dba.Expr("age+#{1}", 1),
		"name":  "bob",
	}, "id = #{1}", 2).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"users\" SET \"age\"=age+$1, \"name\"=$2, \"score\"=score+$3+$4 WHERE\nid = $5"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 5 || args[0] != 1 || args[1] != "bob" || args[2] != 10 || args[3] != 11 || args[4] != 2 {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Expr_IdentifierMacro(t *testing.T) {
	q, _ := newQ(t)
	// SQLExpr 内也能用 @{} 标识符转义
	sql, args, err := q.Update("stats", map[string]any{
		"total": dba.Expr("#{1|quote}+#{2}", "count", 1),
	}, "id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "UPDATE \"stats\" SET \"total\"=\"count\"+$1 WHERE\nid = $2"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 2 || args[0] != 1 || args[1] != 5 {
		t.Errorf("args: %v", args)
	}
}

func TestInsert_Expr(t *testing.T) {
	q, _ := newQ(t)
	sql, args, err := q.Insert("logs", map[string]any{
		"created_at": dba.Expr("NOW()"),
		"msg":        "hello",
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := "INSERT  INTO \"logs\" (\"created_at\", \"msg\") VALUES (NOW(), $1)"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 1 || args[0] != "hello" {
		t.Errorf("args: %v", args)
	}
}

func TestUpdate_Expr_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE counters (id INTEGER PRIMARY KEY, val INTEGER)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT  INTO counters VALUES (1, 10)")

	_, err = q.Update("counters", map[string]any{
		"val": dba.Expr("val+#{1}", 5),
	}, "id = #{1}", 1).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var val int
	db.QueryRow("SELECT val FROM counters WHERE id = 1").Scan(&val)
	if val != 15 {
		t.Errorf("expected 15, got %d", val)
	}
}

func TestVal_Int(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, val INTEGER)")
	db.Exec("INSERT INTO items VALUES (1, 10)")
	db.Exec("INSERT INTO items VALUES (2, 20)")

	count, _, err := dba.Scalar[int](q.Add("SELECT COUNT(1) FROM items"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}

func TestVal_String(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY, name text)")
	db.Exec("INSERT INTO items VALUES (1, 'hello')")

	name, _, err := dba.Scalar[string](q.Add("SELECT name FROM items WHERE id = #{1}", 1))
	if err != nil {
		t.Fatal(err)
	}
	if name != "hello" {
		t.Errorf("expected hello, got %q", name)
	}
}

// TestUpdate_SliceAsSingleParam 值位置的 slice 是单个绑定参数 (展开只属于 IN 语法)。
// 列值传 []byte / json.RawMessage 直接可用, 不需要包装类型。
func TestUpdate_SliceAsSingleParam(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Update("users", data, "id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	wantSQL := "UPDATE \"users\" SET \"name\"=$1, \"tags\"=$2 WHERE\nid = $3"
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 3 {
		t.Errorf("args: got %d %v, want 3", len(args), args)
	}
}

// TestInsert_SliceAsSingleParam 同上, Insert 场景
func TestInsert_SliceAsSingleParam(t *testing.T) {
	q, _ := newQ(t)
	data := map[string]any{
		"name": "极客",
		"tags": []string{"golang", "sql"},
	}
	sql, args, err := q.Insert("users", data).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 值位置 slice 单参数绑定, 列数 = 值数
	wantSQL := `INSERT  INTO "users" ("name", "tags") VALUES ($1, $2)`
	if sql != wantSQL {
		t.Errorf("sql:\n got  %q\n want %q", sql, wantSQL)
	}
	if len(args) != 2 || args[0] != "极客" || !reflect.DeepEqual(args[1], []string{"golang", "sql"}) {
		t.Errorf("args: got %v, want 2 args with tags slice", args)
	}
}

func TestInsert_Exec(t *testing.T) {
	q, db := newQ(t)
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name text)")
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
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name text)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO users VALUES (1, 'alice')")

	_, err = q.Update("users", map[string]any{"name": "bob"}, "id = #{1}", 1).Exec()
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
	_, err := db.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY, name text)")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("INSERT INTO users VALUES (1, 'alice')")
	db.Exec("INSERT INTO users VALUES (2, 'bob')")

	_, err = q.Delete("users", "id = #{1}", 1).Exec()
	if err != nil {
		t.Fatal(err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

// []byte 字段必须作为单个二进制参数绑定, 不得按 IN 场景展开 (曾把字节数组拆成逐字节参数)
func TestInsert_ByteSlice_AsSingleParam(t *testing.T) {
	type bytesUser struct {
		ID   int64  `db:"id"`
		Data []byte `db:"data"`
	}
	q, _ := newQ(t)
	sql, args, err := q.Insert("users", bytesUser{ID: 1, Data: []byte("hello")}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `INSERT  INTO "users" ("id", "data") VALUES ($1, $2)`
	if sql != want {
		t.Errorf("got  %q\nwant %q", sql, want)
	}
	if len(args) != 2 || string(args[1].([]byte)) != "hello" {
		t.Errorf("args: %v", args)
	}
}

// json.RawMessage 别名同样单参数
func TestInsert_JSONRawMessage_SingleParam(t *testing.T) {
	type jsonUser struct {
		ID  int64           `db:"id"`
		Raw json.RawMessage `db:"raw"`
	}
	q, _ := newQ(t)
	raw := json.RawMessage(`{"a":1}`)
	sql, args, err := q.Insert("users", jsonUser{ID: 2, Raw: raw}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || string(args[1].(json.RawMessage)) != `{"a":1}` {
		t.Errorf("args: %v", args)
	}
	_ = sql
}

// 批量插入: 零值字段不导致列集漂移 (omitempty 按行跳列的回归)
func TestBatchInsert_ZeroValueRows(t *testing.T) {
	type batchUser struct {
		ID   int    `db:"id,omitempty"`
		Name string `db:"name"`
		Age  int    `db:"age,omitempty"`
	}
	q, _ := newQ(t)
	users := []batchUser{
		{Name: "alice", Age: 20},
		{Name: "bob"}, // Age=0: 零值行
		{Name: "carol", Age: 30},
	}
	rows := make([]any, len(users))
	for i := range users {
		rows[i] = users[i]
	}
	sql, args, err := q.BatchInsert("users", rows).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 全列一致: id, name, age (零值也插)
	want := "INSERT  INTO \"users\" (\"id\", \"name\", \"age\") VALUES\n($1, $2, $3), ($4, $5, $6), ($7, $8, $9)"
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	if len(args) != 9 {
		t.Errorf("args: %v", args)
	}
}
