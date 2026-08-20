package dba_test

import (
	"strings"
	"testing"

	"github.com/kran/dba"
)

type fetchItem struct {
	ID  int    `db:"id"`
	Tag string `db:"tag"`
}

func setupFetch(t *testing.T) *dba.SQL {
	t.Helper()
	q, db := newQ(t)
	_, err := db.Exec(`CREATE TABLE fe_items (id INTEGER PRIMARY KEY, tag TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		if _, err := db.Exec(`INSERT INTO fe_items VALUES (?, ?)`, i, []string{"a", "b", "c", "d", "e"}[i-1]); err != nil {
			t.Fatal(err)
		}
	}
	return q
}

// ── FetchOne: 严格 0..1 ──────────────────────────────

func TestFetchOne_Found(t *testing.T) {
	q := setupFetch(t)

	item, found, err := q.Add("SELECT * FROM fe_items WHERE id = #{1}", 3).FetchOne[fetchItem]()
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if item.Tag != "c" {
		t.Errorf("expected c, got %q", item.Tag)
	}
}

func TestFetchOne_NotFound(t *testing.T) {
	q := setupFetch(t)

	item, found, err := q.Add("SELECT * FROM fe_items WHERE id = #{1}", 999).FetchOne[fetchItem]()
	if err != nil || found {
		t.Fatalf("expected not found, found=%v err=%v", found, err)
	}
	if item.ID != 0 {
		t.Errorf("expected zero value, got %+v", item)
	}
}

func TestFetchOne_TooManyRows(t *testing.T) {
	q := setupFetch(t)

	_, _, err := q.Add("SELECT * FROM fe_items").FetchOne[fetchItem]()
	if err == nil || !strings.Contains(err.Error(), "more than one row") {
		t.Fatalf("expected too-many-rows error, got %v", err)
	}
}

// LIMIT 1 显式表达"随便取一行", 严格检查不触发
func TestFetchOne_LimitOne(t *testing.T) {
	q := setupFetch(t)

	item, found, err := q.Add("SELECT * FROM fe_items ORDER BY id LIMIT 1").FetchOne[fetchItem]()
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if item.ID != 1 {
		t.Errorf("expected first row, got %+v", item)
	}
}

// ── FetchList: struct 与标量 ──────────────────────────

func TestFetchList(t *testing.T) {
	q := setupFetch(t)

	items, err := q.Add("SELECT * FROM fe_items ORDER BY id").FetchList[fetchItem]()
	if err != nil || len(items) != 5 || items[0].ID != 1 || items[4].ID != 5 {
		t.Fatalf("len=%d err=%v", len(items), err)
	}
}

// 单列标量查询走 FetchList[T] 即可 (sqlx 对多列 + 标量 dest 会报错)
func TestFetchList_ScalarType(t *testing.T) {
	q := setupFetch(t)

	ids, err := q.Add("SELECT id FROM fe_items ORDER BY id").FetchList[int]()
	if err != nil || len(ids) != 5 || ids[0] != 1 || ids[4] != 5 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

// ── FetchOne: 标量 ──────────────────────────────────

func TestFetchOne_ScalarType(t *testing.T) {
	q := setupFetch(t)

	count, found, err := q.Add("SELECT COUNT(1) FROM fe_items").FetchOne[int]()
	if err != nil || !found || count != 5 {
		t.Fatalf("count=%d found=%v err=%v", count, found, err)
	}
}

// ── FetchIndexed / FetchGrouped ──────────────────────

func TestFetchIndexed(t *testing.T) {
	q := setupFetch(t)

	m, err := q.Add("SELECT * FROM fe_items").FetchIndexed[int](func(it fetchItem) int { return it.ID })
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 5 || m[3].Tag != "c" {
		t.Errorf("got %v", m)
	}
}

func TestFetchIndexed_DuplicateKey(t *testing.T) {
	q := setupFetch(t)

	_, err := q.Add("SELECT * FROM fe_items").FetchIndexed[string](func(it fetchItem) string { return "k" })
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestFetchGrouped(t *testing.T) {
	q := setupFetch(t)

	g, err := q.Add("SELECT * FROM fe_items").FetchGrouped[int](func(it fetchItem) int { return it.ID % 2 })
	if err != nil {
		t.Fatal(err)
	}
	if len(g[0]) != 2 || len(g[1]) != 3 {
		t.Errorf("got %v", g)
	}
}

// ── Iter: 惰性迭代器 ─────────────────────────────────

func TestIter_Basic(t *testing.T) {
	q := setupFetch(t)

	var ids []int
	for it, err := range q.Add("SELECT * FROM fe_items ORDER BY id").Iter[fetchItem]() {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	if len(ids) != 5 || ids[0] != 1 || ids[4] != 5 {
		t.Errorf("got ids %v", ids)
	}
}

func TestIter_BreakEarly(t *testing.T) {
	q := setupFetch(t)

	n := 0
	for it, err := range q.Add("SELECT * FROM fe_items ORDER BY id").Iter[fetchItem]() {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if it.ID == 2 {
			break
		}
	}
	if n != 2 {
		t.Errorf("expected 2 iterations, got %d", n)
	}
}

func TestIter_SQLError(t *testing.T) {
	q, _ := newQ(t)

	gotErr := false
	for _, err := range q.Add("SELECT * FROM fe_missing_tbl").Iter[fetchItem]() {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatal("expected error on missing table")
	}
}

func TestIter_ScalarType(t *testing.T) {
	q := setupFetch(t)

	sum := 0
	for v, err := range q.Add("SELECT id FROM fe_items").Iter[int]() {
		if err != nil {
			t.Fatal(err)
		}
		sum += v
	}
	if sum != 15 {
		t.Errorf("expected 15, got %d", sum)
	}
}

// ── FetchRows: 急切原始游标 ──────────────────────────

func TestFetchRows(t *testing.T) {
	q := setupFetch(t)

	rows, err := q.Add("SELECT * FROM fe_items ORDER BY id").FetchRows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var it fetchItem
		if err := rows.StructScan(&it); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Errorf("expected 5 rows, got %d", n)
	}
}
