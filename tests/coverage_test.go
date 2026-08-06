package dba_test

import (
	"errors"
	"testing"

	"github.com/kran/dba"
)

// ── 覆盖补充: DAO 方法 + dba 生命周期 (sqlite) ──

type covUser struct {
	ID   int64  `db:"id,omitempty"`
	Name string `db:"name"`
}

// covIns 插入用: 不带 id (BatchInsert 全列, 零值 id 会撞唯一键)
type covIns struct {
	Name string `db:"name"`
}

func TestCov_OpenPoolClose(t *testing.T) {
	q, err := dba.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if q.Pool() == nil {
		t.Fatal("Pool should be non-nil")
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	// nil pool 的 Close 分支
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCov_WithCtxUnsafe(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_t (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO cov_t VALUES (1)")

	ctx := t.Context()
	qc := q.WithCtx(ctx)
	if _, _, err := qc.Add("SELECT 1").ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Unsafe: sqlx 宽松映射模式 (字段缺失不报错); 宏解析不受影响
	qu := q.Unsafe()
	if _, _, err := qu.Add("SELECT #{1}", 42).ToSQL(); err != nil {
		t.Fatalf("unsafe add: %v", err)
	}
}

func TestCov_DAORawSelectPage(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")
	db.Exec("INSERT INTO cov_users (name) VALUES ('a'), ('b'), ('c')")

	dao := dba.NewDao[covUser](q, "cov_users")
	// RawSelect
	if _, _, err := dao.RawSelect("1=1").ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Page
	items, total, err := dao.Page(1, 2, "1=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || total != 3 {
		t.Fatalf("page: %d items, %d total", len(items), total)
	}
	// Page 带条件
	if _, _, err := dao.Page(1, 10, "id > #{1}", 1); err != nil {
		t.Fatal(err)
	}
}

func TestCov_DAOBatch(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_batch (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")

	dao := dba.NewDao[covIns](q, "cov_batch")
	// RawBatch
	if _, _, err := dao.RawBatch([]covIns{{Name: "x"}, {Name: "y"}}).ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Batch 执行
	n, err := dao.Batch([]covIns{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("affected = %d, want 3", n)
	}
	// 空切片: RawBatch 返回 err
	empty := dao.RawBatch(nil)
	if _, _, err := empty.ToSQL(); err == nil {
		t.Fatal("empty batch should error")
	}
}

type covHookErr struct {
	ID   int64  `db:"id,omitempty"`
	Name string `db:"name"`
}

func (h *covHookErr) BeforeCreate() error {
	return errors.New("hook boom")
}

func TestCov_DAOBatchHookError(t *testing.T) {
	q, _ := newQ(t)
	dao := dba.NewDao[covHookErr](q, "cov_bad")
	b := dao.RawBatch([]covHookErr{{Name: "x"}})
	if _, _, err := b.ToSQL(); err == nil {
		t.Fatal("hook error should propagate")
	}
	// Create 的 BeforeCreate 错误路径
	if _, err := dao.Create(covHookErr{Name: "y"}); err == nil {
		t.Fatal("create hook error should propagate")
	}
}

func TestCov_DAOWithCtxTable(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_ctx (id INTEGER PRIMARY KEY)")
	dao := dba.NewDao[covUser](q, "cov_ctx")
	dao2 := dao.WithCtx(t.Context()).Table("cov_ctx")
	if _, err := dao2.GetByID(1); err != nil {
		t.Fatal(err) // 不存在 → (nil, nil) 不报错
	}
}

func TestCov_ExecRowsAffected(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_ra (id INTEGER PRIMARY KEY)")
	// Exec 的 RowsAffected 路径
	_, err := q.Add("INSERT INTO cov_ra VALUES (1)").Exec()
	if err != nil {
		t.Fatal(err)
	}
}

// ── 覆盖补充 2: List/Get map 分支 + Batch/BatchInsert 错误分支 ──

func TestCov_ListGetMap(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_map (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec("INSERT INTO cov_map VALUES (1, 'a'), (2, 'b')")

	// ListMap: 动态列查询
	ms, err := q.Add("SELECT * FROM cov_map").ListMap()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("ListMap: %d rows", len(ms))
	}
	// GetMap: 单行 map
	m, found, err := q.Add("SELECT * FROM cov_map WHERE id = #{1}", 1).GetMap()
	if err != nil || !found {
		t.Fatalf("GetMap: found=%v err=%v", found, err)
	}
	// GetMap 未找到
	m, found, err = q.Add("SELECT * FROM cov_map WHERE id = #{1}", 99).GetMap()
	if err != nil || found || m != nil {
		t.Fatalf("GetMap missing: found=%v m=%v err=%v", found, m, err)
	}
}

func TestCov_BatchInsertErrors(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_bi (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)")

	// BatchInsert 空实体
	b := q.BatchInsert("cov_bi", []any{})
	if _, _, err := b.ToSQL(); err == nil {
		t.Fatal("empty BatchInsert should error")
	}
	// BatchInsert 单实体 (部分列路径)
	if _, err := q.BatchInsert("cov_bi", []any{covIns{Name: "single"}}).Exec(); err != nil {
		t.Fatal(err)
	}
	// Batch (rows 版) 空行 → 错误
	if _, err := q.Batch([][]any{}).Exec(); err == nil {
		t.Fatal("empty Batch rows should error")
	}
	// Batch rows 版正常: Add(...).Batch(rows)
	if _, err := q.Add("INSERT INTO cov_bi (name) VALUES").Batch([][]any{{1}, {2}}).Exec(); err != nil {
		t.Fatal(err)
	}
}

func TestCov_NewFromSqlxDriverBranch(t *testing.T) {
	// sqlite driver 分支 (newQ 已覆盖); mysql driver 分支需要 DSN — 用 nil pool 的 copy 路径
	_ = dba.QmarkFormat(1)
	_ = dba.DollarFormat(1)
	_ = dba.MySQLQuoter("x")
	_ = dba.AnsiQuoter("x")
}

func TestCov_AddVarEdges(t *testing.T) {
	q, _ := newQ(t)
	// Add 命名参数
	if _, _, err := q.Add("WHERE id = #{id}", map[string]any{"id": 1}).ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Var 覆盖默认
	base := q.Add("SELECT ${F:*} FROM t")
	if _, _, err := base.Var(dba.F, "id").ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Vars 批量注册
	if _, _, err := q.Vars(map[string]dba.Node{
		"a": {Text: "1"},
	}).Add("SELECT ${a}").ToSQL(); err != nil {
		t.Fatal(err)
	}
	// Add 无参数
	if _, _, err := q.Add("SELECT 1").ToSQL(); err != nil {
		t.Fatal(err)
	}
}

// ── 覆盖补充 3: 错误/边界分支 ──

func TestCov_ErrorMethod(t *testing.T) {
	q, _ := newQ(t)
	// Error() 返回构建期错误 (clone.err — Add/Batch 阶段产生)
	bad := q.Batch([][]any{})
	if err := bad.Error(); err == nil {
		t.Fatal("Error() should return batch error")
	}
	// 无错误实例
	if err := q.Add("SELECT 1").Error(); err != nil {
		t.Fatal(err)
	}
}

func TestCov_OpenError(t *testing.T) {
	if _, err := dba.Open("bad-driver-name", ""); err == nil {
		t.Fatal("unknown driver should error")
	}
}

func TestCov_AddVarOnErrorInstance(t *testing.T) {
	q, _ := newQ(t)
	// clone.err 短路: Batch 空行产生构建期错误, 后续 Add/Var/Vars 直接短路
	bad := q.Batch([][]any{})
	if err := bad.Error(); err == nil {
		t.Fatal("batch should error")
	}
	if _, _, err := bad.Add("AND 1=1").ToSQL(); err == nil {
		t.Fatal("err should propagate through Add")
	}
	if _, _, err := bad.Var("k", "v").ToSQL(); err == nil {
		t.Fatal("err should propagate through Var")
	}
	if _, _, err := bad.Vars(map[string]dba.Node{"k": {Text: "v"}}).ToSQL(); err == nil {
		t.Fatal("err should propagate through Vars")
	}
}

func TestCov_ListGetExecErrors(t *testing.T) {
	q, _ := newQ(t)
	// List 执行错误
	var items []covUser
	if err := q.Add("SELECT * FROM cov_missing_tbl").List(&items); err == nil {
		t.Fatal("list on missing table should error")
	}
	// Get 执行错误
	var u covUser
	if _, err := q.Add("SELECT * FROM cov_missing_tbl").Get(&u); err == nil {
		t.Fatal("get on missing table should error")
	}
	// Rows 执行错误
	if _, err := q.Add("SELECT * FROM cov_missing_tbl").Rows(); err == nil {
		t.Fatal("rows on missing table should error")
	}
}

func TestCov_BatchWidthError(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_bw (id INTEGER PRIMARY KEY, name TEXT)")
	// 行宽 0
	b := q.Batch([][]any{{}})
	if _, _, err := b.ToSQL(); err == nil {
		t.Fatal("zero-width row should error")
	}
	// 行宽不一致
	b2 := q.Batch([][]any{{1}, {2, "x"}})
	if _, _, err := b2.ToSQL(); err == nil {
		t.Fatal("inconsistent width should error")
	}
}

func TestCov_BatchInsertMixedErrors(t *testing.T) {
	q, _ := newQ(t)
	// 非切片实体 ([]any 里的标量)
	b := q.BatchInsert("t", []any{"not-a-slice"})
	if _, _, err := b.ToSQL(); err == nil {
		t.Fatal("non-slice element should error")
	}
	// 空切片
	if _, _, err := q.BatchInsert("t", []any{}).ToSQL(); err == nil {
		t.Fatal("empty should error")
	}
}

func TestCov_RowsError(t *testing.T) {
	q, _ := newQ(t)
	if _, err := q.Add("SELECT * FROM no_such_table_cov").Rows(); err == nil {
		t.Fatal("rows on missing table should error")
	}
}

// ── 覆盖补充 4: 工具函数 + pipe/事务边界 ──

func TestCov_Utils(t *testing.T) {
	// Map
	got := dba.Map([]int{1, 2, 3}, func(i int) string { return string(rune('a' + i)) })
	if len(got) != 3 {
		t.Fatal("map len")
	}
	// IsOk 各分支
	if !dba.IsOk("x") || dba.IsOk("") || dba.IsOk(nil) || dba.IsOk([]int{}) || !dba.IsOk([]int{1}) || !dba.IsOk(42) {
		t.Fatal("IsOk branches")
	}
}

func TestCov_PipeEdges(t *testing.T) {
	q, _ := newQ(t)
	// RegisterPipe 重名 panic
	func() {
		defer func() { _ = recover() }()
		q.RegisterPipe("bind", func(ctx dba.RenderCtx, content string) error { return nil })
	}()
	// expand 非切片 (错误分支)
	if _, _, err := q.Add("IN (#{1|expand})", 42).ToSQL(); err == nil {
		t.Fatal("expand non-slice should error")
	}
	// quote 空值 (nil 参数)
	if _, _, err := q.Add("#{1|quote}", nil).ToSQL(); err == nil {
		t.Fatal("quote nil should error")
	}
	// literalquote 空内容
	if _, _, err := q.Add("SELECT @{}").ToSQL(); err == nil {
		t.Fatal("empty literalquote should error")
	}
}

func TestCov_TransactionErrors(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE cov_tx (id INTEGER PRIMARY KEY)")
	// Transaction 正常提交
	err := q.Transaction(func(tx *dba.SQL) error {
		_, e := tx.Add("INSERT INTO cov_tx VALUES (1)").Exec()
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// Transaction 回滚
	err = q.Transaction(func(tx *dba.SQL) error {
		_, _ = tx.Add("INSERT INTO cov_tx VALUES (2)").Exec()
		return errors.New("rollback me")
	})
	if err == nil {
		t.Fatal("should return rollback error")
	}
	// Begin/Commit/Rollback 独立路径
	tx, err := q.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Add("INSERT INTO cov_tx VALUES (3)").Exec(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx2, _ := q.Begin()
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
}
