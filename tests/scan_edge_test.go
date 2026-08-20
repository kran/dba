package dba_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"
)

// 扫描分流 (scanRow/isScannableForScan) 的边界钉住:
//   - 指针类型 T (*User): 全层级解引用, struct 路径先分配内层指针
//   - Scanner 实现者 → Scan 路径
//   - time.Time (无导出字段 struct) → Scan 路径, 不 panic 不报 missing column
//   - 无导出字段 struct → Scan 路径 (driver 转换错误, 而非 StructScan 错误)

func TestScanEdge_PointerT(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE se (id INTEGER PRIMARY KEY, tag TEXT)`)
	db.Exec(`INSERT INTO se VALUES (1, 'a'), (2, 'b')`)

	// FetchOne[*T]: StructScan 收到 **T, 需全层级解引用 + 内层分配
	u, found, err := q.Add("SELECT * FROM se WHERE id = #{1}", 1).FetchOne[*fetchItem]()
	if err != nil || !found {
		t.Fatalf("FetchOne[*T] found=%v err=%v", found, err)
	}
	if u.Tag != "a" {
		t.Errorf("got %+v", u)
	}

	// FetchList[*T] 对照
	us, err := q.Add("SELECT * FROM se ORDER BY id").FetchList[*fetchItem]()
	if err != nil || len(us) != 2 || us[1].Tag != "b" {
		t.Fatalf("FetchList[*T] len=%d err=%v", len(us), err)
	}

	// Iter[*T] 对照: 每行全新指针
	for v, err := range q.Add("SELECT * FROM se ORDER BY id").Iter[*fetchItem]() {
		if err != nil {
			t.Fatal(err)
		}
		if v.ID != 1 {
			t.Errorf("expected first id 1, got %+v", v)
		}
		break
	}

	// FetchOne[*int] 标量指针链: 由 driver convertAssign 分配
	n, found, err := q.Add("SELECT id FROM se WHERE id = #{1}", 2).FetchOne[*int]()
	if err != nil || !found || n == nil || *n != 2 {
		t.Fatalf("FetchOne[*int] n=%v found=%v err=%v", n, found, err)
	}
}

func TestScanEdge_NullString(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE se2 (v TEXT)`)
	db.Exec(`INSERT INTO se2 VALUES (NULL)`)

	// Scanner 实现者 (*T 方法集) → Scan 路径
	ns, found, err := q.Add("SELECT v FROM se2").FetchOne[sql.NullString]()
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if ns.Valid || ns.String != "" {
		t.Errorf("expected invalid NULL, got %+v", ns)
	}
}

func TestScanEdge_Time(t *testing.T) {
	q, _ := newQ(t)

	// time.Time: 无导出字段 struct → 必须走 Scan 路径。
	// driver 层可能报转换错误 (modernc 默认不解析字符串为 time.Time),
	// 但绝不能是 StructScan 的 "missing destination name" (那说明路由错了)。
	tt, found, err := q.Add("SELECT '2024-01-01 00:00:00' AS t").FetchOne[time.Time]()
	if err != nil {
		if strings.Contains(err.Error(), "missing destination name") {
			t.Fatalf("routed to StructScan unexpectedly: %v", err)
		}
		return // driver 级转换错误: 路由正确
	}
	if !found || tt.Year() != 2024 {
		t.Errorf("got %v", tt)
	}
}

func TestScanEdge_NoExportedFields(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE se3 (id INTEGER PRIMARY KEY)`)
	db.Exec(`INSERT INTO se3 VALUES (1)`)

	type opaque struct{ x int }
	// 无导出字段 struct → Scan 路径: 得到 driver 转换错误, 而非 StructScan 的
	// missing destination name (那才是路由 bug)
	_, _, err := q.Add("SELECT * FROM se3").FetchOne[opaque]()
	if err == nil {
		t.Fatal("expected scan conversion error for opaque struct")
	}
	if strings.Contains(err.Error(), "missing destination name") {
		t.Fatalf("routed to StructScan unexpectedly: %v", err)
	}
}

func TestFetchOneMap_TooManyRows(t *testing.T) {
	q, db := newQ(t)
	db.Exec(`CREATE TABLE se4 (id INTEGER PRIMARY KEY)`)
	db.Exec(`INSERT INTO se4 VALUES (1), (2)`)

	// 与 FetchOne 同契约: 严格 0..1
	_, found, err := q.Add("SELECT * FROM se4").FetchOneMap()
	if err == nil || !strings.Contains(err.Error(), "more than one row") {
		t.Fatalf("expected too-many-rows error, found=%v err=%v", found, err)
	}
}
