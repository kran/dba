package dba_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kran/dba"
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

// 自定义 Pager 全链路: FetchPage 追加的子句来自 pager 渲染, 而非硬编码 LIMIT
func TestPage_CustomPager(t *testing.T) {
	q := setupPageTable(t)

	var queries []string
	q = q.SetLogger(func(_ context.Context, _ time.Time, query string, _ []any, _ error) {
		queries = append(queries, query)
	}).Pager(func(limit, offset string) string {
		return "LIMIT " + limit + " OFFSET " + offset + " /*custom-pager*/"
	})

	query := q.Add("SELECT ${F:*} FROM page_items ORDER BY id")
	items, total, err := query.FetchPage[PageItem](2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 || len(items) != 10 || items[0].ID != 11 {
		t.Errorf("total=%d len=%d first=%+v", total, len(items), items[0])
	}

	// count 查询 (第一) 不含分页子句; data 查询 (第二) 含自定义 pager 产物
	if len(queries) != 2 {
		t.Fatalf("expected 2 executed queries, got %d", len(queries))
	}
	if strings.Contains(queries[0], "custom-pager") {
		t.Errorf("count query should not carry pager clause: %q", queries[0])
	}
	if !strings.Contains(queries[1], "/*custom-pager*/") {
		t.Errorf("data query missing custom pager clause: %q", queries[1])
	}
}

// count 查询必须清空 ${order} 排序槽: 严格方言 (PG/mssql) 下聚合查询带
// ORDER BY 源列是硬错误, 且 SQL Server 的 OFFSET...FETCH 强制数据查询带
// ORDER BY —— 两者只能靠 ${order} 槽同时满足
func TestPage_CountQueryClearsOrderSlot(t *testing.T) {
	q := setupPageTable(t)

	var queries []string
	q = q.SetLogger(func(_ context.Context, _ time.Time, query string, _ []any, _ error) {
		queries = append(queries, query)
	})

	query := q.Add("SELECT ${F:*} FROM page_items ${order:ORDER BY id DESC}")
	if _, total, err := query.FetchPage[PageItem](1, 10); err != nil || total != 25 {
		t.Fatalf("total=%d err=%v", total, err)
	}

	if len(queries) != 2 {
		t.Fatalf("expected 2 executed queries, got %d", len(queries))
	}
	countSQL := queries[0]
	dataSQL := queries[1]

	if strings.Contains(countSQL, "ORDER BY") {
		t.Errorf("count query must not carry ORDER BY: %q", countSQL)
	}
	if !strings.Contains(dataSQL, "ORDER BY id DESC") {
		t.Errorf("data query missing ORDER BY: %q", dataSQL)
	}
}
