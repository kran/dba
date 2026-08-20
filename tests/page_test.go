package dba_test

import (
	"github.com/kran/dba"
	"testing"
)

type PageItem struct {
	ID  int    `db:"id"`
	Val int    `db:"val"`
	Cat string `db:"cat"`
}

func setupPageTable(t *testing.T) *dba.SQL {
	t.Helper()
	q, db := newQ(t)
	db.Exec(`CREATE TABLE page_items (id INTEGER PRIMARY KEY AUTOINCREMENT, val INTEGER, cat TEXT)`)
	for i := 1; i <= 25; i++ {
		cat := "a"
		if i > 15 {
			cat = "b"
		}
		db.Exec("INSERT INTO page_items (val, cat) VALUES (?, ?)", i, cat)
	}
	return q
}

func TestPage_Basic(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items ORDER BY id")
	items, total, err := query.FetchPage[PageItem](1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 {
		t.Errorf("expected total 25, got %d", total)
	}
	if len(items) != 10 {
		t.Errorf("expected 10 items, got %d", len(items))
	}
	if items[0].ID != 1 {
		t.Errorf("expected first id 1, got %d", items[0].ID)
	}
}

func TestPage_SecondPage(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items ORDER BY id")
	items, total, err := query.FetchPage[PageItem](2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 {
		t.Errorf("expected total 25, got %d", total)
	}
	if len(items) != 10 {
		t.Errorf("expected 10 items, got %d", len(items))
	}
	if items[0].ID != 11 {
		t.Errorf("expected first id 11, got %d", items[0].ID)
	}
}

func TestPage_LastPage(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items ORDER BY id")
	items, total, err := query.FetchPage[PageItem](3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 {
		t.Errorf("expected total 25, got %d", total)
	}
	if len(items) != 5 {
		t.Errorf("expected 5 items, got %d", len(items))
	}
}

func TestPage_WithWhere(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items WHERE cat = #{1} ORDER BY id", "a")
	items, total, err := query.FetchPage[PageItem](1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 15 {
		t.Errorf("expected total 15, got %d", total)
	}
	if len(items) != 10 {
		t.Errorf("expected 10 items, got %d", len(items))
	}
}

func TestPage_WithJoin(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT)`)
	db.Exec(`CREATE TABLE books (id INTEGER PRIMARY KEY AUTOINCREMENT, author_id INTEGER, title TEXT)`)
	db.Exec(`INSERT INTO authors VALUES (1, 'alice'), (2, 'bob')`)
	for i := 0; i < 12; i++ {
		db.Exec("INSERT INTO books (author_id, title) VALUES (1, ?)", "book-"+string(rune('a'+i)))
	}
	db.Exec("INSERT INTO books (author_id, title) VALUES (2, 'other')")

	type BookRow struct {
		ID     int    `db:"id"`
		Title  string `db:"title"`
		Author string `db:"author"`
	}

	query := q.Add("SELECT ${F:b.id, b.title, a.name AS author} FROM books b JOIN authors a ON b.author_id = a.id").
		Add("WHERE a.name = #{1}", "alice").
		Add("ORDER BY b.id")

	items, total, err := query.FetchPage[BookRow](2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 {
		t.Errorf("expected total 12, got %d", total)
	}
	if len(items) != 5 {
		t.Errorf("expected 5 items, got %d", len(items))
	}
	if items[0].Author != "alice" {
		t.Errorf("expected alice, got %q", items[0].Author)
	}
}

func TestPage_InvalidParams(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items ORDER BY id")

	// page < 1 报错 (不再静默钳制)
	if _, _, err := query.FetchPage[PageItem](0, 10); err == nil {
		t.Fatal("expected error for page 0")
	}
	// size < 1 报错
	if _, _, err := query.FetchPage[PageItem](1, -1); err == nil {
		t.Fatal("expected error for size -1")
	}
	// 边界值 1/1 合法
	if _, _, err := query.FetchPage[PageItem](1, 1); err != nil {
		t.Fatalf("page=1 size=1 should be valid, got %v", err)
	}
}

func TestPage_EmptyResult(t *testing.T) {
	q := setupPageTable(t)

	query := q.Add("SELECT ${F:*} FROM page_items WHERE cat = #{1} ORDER BY id", "nonexistent")
	items, total, err := query.FetchPage[PageItem](1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("expected total 0, got %d", total)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}
