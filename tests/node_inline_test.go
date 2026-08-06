package dba_test

import (
	"testing"

	"github.com/kran/dba"
)

// Node 参数内联 (bind 管道认识 Node —— 参数即子树)

func TestNodeInline_Bind(t *testing.T) {
	q, _ := newQ(t)
	// 值位置 Node 内联
	sql, args, err := q.Add("INSERT INTO t (a) VALUES (#{1})", dba.Expr("NOW()")).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "INSERT INTO t (a) VALUES (NOW())" || len(args) != 0 {
		t.Fatalf("bind node: %q %v", sql, args)
	}
	// Node 带参数: 子参数自动连续编号
	sql, args, err = q.Add("INSERT INTO t (a, b) VALUES (#{1}, #{2})",
		dba.Expr("x + #{1}", 10), 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "INSERT INTO t (a, b) VALUES (x + $1, $2)" || len(args) != 2 || args[0] != 10 || args[1] != 5 {
		t.Fatalf("bind node with args: %q %v", sql, args)
	}
}

func TestNodeInline_Nested(t *testing.T) {
	q, _ := newQ(t)
	// Node 的 Args 里再有 Node (隐式树, 递归渲染)
	inner := dba.Expr("score + #{1}", 10)
	outer := dba.Expr("GREATEST(#{1}, 0)", inner)
	sql, args, err := q.Add("SELECT #{1} FROM t", outer).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "SELECT GREATEST(score + $1, 0) FROM t" || len(args) != 1 || args[0] != 10 {
		t.Fatalf("nested node: %q %v", sql, args)
	}
}

func TestNodeInline_Expand(t *testing.T) {
	q, _ := newQ(t)
	// expand 的元素可以是 Node: IN (expr, ?)
	sql, args, err := q.Add("WHERE id IN (#{1|expand})",
		[]any{dba.Expr("(SELECT id FROM banned)"), 5}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE id IN ((SELECT id FROM banned), $1)" || len(args) != 1 || args[0] != 5 {
		t.Fatalf("expand node: %q %v", sql, args)
	}
}

func TestNodeInline_Raw(t *testing.T) {
	q, _ := newQ(t)
	// raw 遇 Node → 内联 (不输出 %v 垃圾)
	sql, args, err := q.Add("WHERE x > #{1|raw}", dba.Expr("NOW() - INTERVAL '1 day'")).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE x > NOW() - INTERVAL '1 day'" || len(args) != 0 {
		t.Fatalf("raw node: %q %v", sql, args)
	}
}

func TestNodeInline_NamedMapValue(t *testing.T) {
	q, _ := newQ(t)
	// 命名参数源 (map) 的字段值可以是 Node
	sql, args, err := q.Add("WHERE created_at > #{ts}", map[string]any{
		"ts": dba.Expr("NOW() - INTERVAL '1 hour'"),
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if sql != "WHERE created_at > NOW() - INTERVAL '1 hour'" || len(args) != 0 {
		t.Fatalf("named map node: %q %v", sql, args)
	}
}

func TestNodeInline_InsertUpdate(t *testing.T) {
	q, _ := newQ(t)
	// Insert: 列值是 Node
	_, args, err := q.Insert("stats", map[string]any{
		"views": dba.Expr("views + #{1}", 1),
		"name":  "x",
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "x" || args[1] != 1 {
		t.Fatalf("insert node args: %v", args)
	}
	// Update: 列值是 Node
	_, args, err = q.Update("stats", map[string]any{
		"views": dba.Expr("views + #{1}", 10),
	}, "id = #{1}", 1).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != 10 || args[1] != 1 {
		t.Fatalf("update node args: %v", args)
	}
}

func TestNodeInline_BatchValue(t *testing.T) {
	q, _ := newQ(t)
	// Batch 值组里的 Node
	_, args, err := q.Add("INSERT INTO t (a, b) VALUES").Batch([][]any{
		{dba.Expr("NOW()"), 1},
		{dba.Expr("NOW()"), 2},
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != 1 || args[1] != 2 {
		t.Fatalf("batch node args: %v", args)
	}
}

// 深度护栏: ${a} 自引用不再栈溢出 (现有 bug 修复验证)

func TestRenderDepthLimit(t *testing.T) {
	q, _ := newQ(t)
	// ${a} 自引用
	cyclic := q.Var("a", "${a}")
	if _, _, err := cyclic.Add("SELECT ${a}").ToSQL(); err == nil {
		t.Fatal("cyclic var should error (depth limit)")
	}
	// Node 嵌套自引用? Node 值不可变 — 构造不了环 — 只测变量环
	// 长链 (合法但深) — 64 内正常
	deep := q
	for i := 0; i < 10; i++ {
		deep = deep.Var(string(rune('a'+i)), "${"+string(rune('a'+i+1))+":}")
	}
	if _, _, err := deep.Add("SELECT 1").ToSQL(); err != nil {
		t.Fatalf("deep chain should render: %v", err)
	}
}

// Update 的 where 片段参数编号独立于 SET 片段 (per-node 参数作用域)。
func TestNodeInline_UpdateWhereIndependent(t *testing.T) {
	q, _ := newQ(t)
	_, args, err := q.Update("stats", map[string]any{
		"views": dba.Expr("views + #{1}", 100), // SET 片段参数 (Node 子参数)
	}, "id = #{1} AND status = #{2}", 7, "active").ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// Node 子参数 $1=100; where 参数 $2=7, $3=active — 编号独立连续
	if len(args) != 3 || args[0] != 100 || args[1] != 7 || args[2] != "active" {
		t.Fatalf("update where args: %v", args)
	}
}
