package dba

// dba_super_test.go — 黄金输出超级测试。
//
// 一个测试函数覆盖 builder 的全部纯构建路径 (不需要数据库连接):
//
//	模板层: #{位置} / #{命名:map} / #{命名:struct db tag} / ${var} / ${var:默认值} /
//	       ${var} 嵌套递归 + 递归后参数作用域恢复 / @{标识符} / !{raw} / |pipe 覆盖 /
//	       自定义管道 / 自定义宏前缀 / per-node 参数编号独立
//	Node 统一 ("参数即子树"): bind 内联 / expand 元素内联 / raw 内联 /
//	       命名参数源字段值为 Node 时内联
//	生成器: Select(${F:*}) / Insert(map+struct, omitempty, ${I:} 空/覆盖) /
//	       Update / Delete / Batch(含 Node 值) / BatchInsert
//	方言:   DollarFormat+AnsiQuoter 主线; QmarkFormat+MySQLQuoter 渲染期切换
//	不可变: 同一 base 分叉 count/data (Page 协议), base 不受影响
//	错误:   未定义变量 / 递归深度(自引用环) / 未知管道 / 索引越界 / 无命名源 /
//	       命名缺失 / expand 非切片 / batch 宽度不齐 / err 沿链传播 /
//	       保留宏前缀 / 空管道名
//
// 断言是精确字符串比对 (含 `INSERT  INTO` 的双空格 —— ${I:} 空展开的真实产物)。
// 任何失败都意味着渲染行为发生了漂移, 而不是测试写松了。
//
// 不覆盖 (需要真实连接): List/Get/Exec/Rows/Scalar/Page 的执行侧、事务、Unsafe。

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
)

// newTestSQL 构造无连接 builder: 只需渲染四件套 (pipes/macros/quoter/formater)。
func newTestSQL() *SQL {
	return &SQL{
		varNodes:  map[string]Node{},
		pipes:     maps.Clone(defaultPipes),
		macros:    maps.Clone(defaultMacros),
		quoter:    AnsiQuoter,
		formatter: DollarFormat,
	}
}

func checkSQL(t *testing.T, label string, q *SQL, wantSQL string, wantArgs ...any) {
	t.Helper()
	gotSQL, gotArgs, err := q.ToSQL()
	if err != nil {
		t.Errorf("%s: unexpected error: %v", label, err)
		return
	}
	if gotSQL != wantSQL {
		t.Errorf("%s: sql mismatch\n got: %s\nwant: %s", label, gotSQL, wantSQL)
	}
	if len(wantArgs) == 0 {
		wantArgs = nil
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("%s: args mismatch\n got: %#v\nwant: %#v", label, gotArgs, wantArgs)
	}
}

func checkErr(t *testing.T, label string, q *SQL, substr string) {
	t.Helper()
	_, _, err := q.ToSQL()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Errorf("%s: want error containing %q, got: %v", label, substr, err)
	}
}

type superUser struct {
	Email string `db:"email"`
}

type superPost struct {
	Title string `db:"title"`
	Views int    `db:"views,omitempty"`
}

type superKV struct {
	A int `db:"a"`
	B int `db:"b"`
}

func TestBuilderSuper(t *testing.T) {
	db := newTestSQL()

	// ── 1. 超级 SELECT: 一条链吃掉整个模板语言 ─────────────────────────
	//
	// 占位符全局递增 ($1..$15), 但每个 Add 片段的 #{n} 从 1 数 —— per-node 作用域。
	q := db.
		RegisterPipe("upper", func(ctx RenderCtx, content string) error {
			v, err := ctx.Resolve(content)
			if err != nil {
				return err
			}
			return ctx.Bind(strings.ToUpper(fmt.Sprint(v)))
		}).
		RegisterMacro('^', "upper").
		Select("users", "age > #{1}", 18).                      // ${F:*} 默认展开 + 位置参数
		AddIf(false, "AND skipped = #{1}", 0).                  // 假分支: 不产生节点
		AddIf(true, "AND name = #{name}", H{"name": "bob"}).    // 命名参数: map 源
		Add("AND email = #{email}", superUser{Email: "x@y.z"}). // 命名参数: struct db tag
		Add("AND id IN (#{1|expand})",                          // expand + 元素为 Node → 内联子查询
											[]any{1, Expr("(SELECT id FROM vips WHERE lvl > #{1})", 9), 3}).
		Add("AND created_at > !{1}", "NOW()").                              // raw: 普通字符串原样注入
		Add("AND d >= !{1}", Expr("DATE #{1}", "2026-08-06")).              // raw: Node → 内联渲染 (带自己的参数)
		Add("AND updated_at < #{1}", Expr("NOW() - INTERVAL #{1} DAY", 7)). // bind: Node 即子树
		Add("AND nick = ^{1}", "bob").                                      // 自定义宏 → 自定义管道
		Add("AND #{1|quote} IS NOT NULL", "deleted_at").                    // quote: 参数值作为标识符
		Add("AND @{group} = #{1}", "admin").                                // literalquote: 字面量标识符
		Add("AND ${scope}").                                                // 变量递归 (scope → org)
		Vars(map[string]Node{
			// scope 里 ${org} 之后再次使用 #{1}: 验证递归返回后参数作用域恢复
			"scope": Expr("t = #{1} AND ${org} AND t2 = #{1}", "acme"),
			"org":   Expr("org_id = #{1}", "a1"),
		}).
		Add("ORDER BY ${orderBy:id DESC}"). // 未定义变量 → 默认值兜底
		Add("LIMIT #{1} OFFSET #{2}", 10, 20)

	wantSQL := strings.Join([]string{
		`SELECT * FROM "users" WHERE age > $1`,
		`AND name = $2`,
		`AND email = $3`,
		`AND id IN ($4, (SELECT id FROM vips WHERE lvl > $5), $6)`,
		`AND created_at > NOW()`,
		`AND d >= DATE $7`,
		`AND updated_at < NOW() - INTERVAL $8 DAY`,
		`AND nick = $9`,
		`AND "deleted_at" IS NOT NULL`,
		`AND "group" = $10`,
		`AND t = $11 AND org_id = $12 AND t2 = $13`,
		`ORDER BY id DESC`,
		`LIMIT $14 OFFSET $15`,
	}, "\n")
	checkSQL(t, "super select", q, wantSQL,
		18, "bob", "x@y.z", 1, 9, 3, "2026-08-06", 7, "BOB",
		"admin", "acme", "a1", "acme", 10, 20)

	// ── 2. 命名参数源的字段值是 Node → 同样内联 (有意行为, 非巧合) ────────
	checkSQL(t, "named source node inline",
		db.Add("SELECT #{v}", H{"v": Expr("NOW()")}),
		"SELECT NOW()")

	// ── 3. 方言在渲染期生效: 同一模板换 quoter/formater ──────────────────
	checkSQL(t, "mysql dialect",
		db.Quoter(MySQLQuoter).Formatter(QmarkFormat).
			Add("SELECT @{name} FROM t WHERE a = #{1} AND b = #{2}", 1, 2),
		"SELECT `name` FROM t WHERE a = ? AND b = ?", 1, 2)

	// ── 4. Insert: map 源 (键排序) + Node 值内联 + ${I:} 空默认 (注意双空格) ─
	checkSQL(t, "insert map+node",
		db.Insert("articles", H{"title": "hi", "created": Expr("NOW()")}),
		`INSERT  INTO "articles" ("created", "title") VALUES (NOW(), $1)`, "hi")

	// ${I:} 被覆盖为 IGNORE
	checkSQL(t, "insert ignore",
		db.Var(I, "IGNORE").Insert("articles", H{"title": "hi"}),
		`INSERT IGNORE INTO "articles" ("title") VALUES ($1)`, "hi")

	// struct 源 + omitempty: 零值列被剔除
	checkSQL(t, "insert struct omitempty",
		db.Insert("posts", superPost{Title: "t"}),
		`INSERT  INTO "posts" ("title") VALUES ($1)`, "t")

	// ── 5. Update: SET 中 Node 内联; WHERE 片段 #{1} 独立编号 (渲染为 $3) ──
	checkSQL(t, "update node+scope",
		db.Update("users", H{"cnt": Expr("cnt + #{1}", 1), "name": "n"}, "id = #{1}", 5),
		"UPDATE \"users\" SET \"cnt\"=cnt + $1, \"name\"=$2 WHERE\nid = $3",
		1, "n", 5)

	// ── 6. Delete ────────────────────────────────────────────────────────
	checkSQL(t, "delete",
		db.Delete("users", "id = #{1}", 9),
		"DELETE FROM \"users\" WHERE\nid = $1", 9)

	// ── 7. Batch: 值组生成 + Node 值 (DEFAULT) 内联不占位 ────────────────
	checkSQL(t, "batch with node",
		db.Add("INSERT INTO t (a, b) VALUES").Batch([][]any{{1, Expr("DEFAULT")}, {3, 4}}),
		"INSERT INTO t (a, b) VALUES\n($1, DEFAULT), ($2, $3)", 1, 3, 4)

	// ── 8. BatchInsert: 实体反射 → 全列 + ${I:} + Batch ──────────────────
	checkSQL(t, "batch insert",
		db.BatchInsert("t", []any{superKV{1, 2}, superKV{3, 4}}),
		"INSERT  INTO \"t\" (\"a\", \"b\") VALUES\n($1, $2), ($3, $4)", 1, 2, 3, 4)

	// ── 9. 不可变分叉: Page 协议 (同一 base → count / data 两条查询) ──────
	base := db.Select("users", "1=1")
	checkSQL(t, "fork count", base.Var(F, "COUNT(1)"),
		`SELECT COUNT(1) FROM "users" WHERE 1=1`)
	checkSQL(t, "fork data", base.Add("LIMIT #{1}", 10),
		"SELECT * FROM \"users\" WHERE 1=1\nLIMIT $1", 10)
	checkSQL(t, "fork base unchanged", base,
		`SELECT * FROM "users" WHERE 1=1`)

	// ── 10. 渲染期错误 ────────────────────────────────────────────────────
	checkErr(t, "undefined var", db.Add("${nope}"), "undefined variable")
	checkErr(t, "self cycle depth", db.Var("a", "${a}").Add("${a}"), "depth")
	checkErr(t, "unknown pipe", db.Add("#{1|nosuch}", 1), "unknown pipe")
	checkErr(t, "index out of bounds", db.Add("#{2}", 1), "out of bounds")
	checkErr(t, "named without args", db.Add("#{name}"), "no args")
	checkErr(t, "named missing key", db.Add("#{x}", H{"y": 1}), "not found")
	checkErr(t, "expand non-slice", db.Add("#{1|expand}", 5), "expand pipe requires")

	// ── 11. 构建期错误与 err 沿链传播 ────────────────────────────────────
	if err := db.Batch([][]any{{1}, {2, 3}}).Error(); err == nil ||
		!strings.Contains(err.Error(), "row 1") {
		t.Errorf("batch width mismatch: got %v", err)
	}
	if err := db.Batch(nil).Error(); err == nil ||
		!strings.Contains(err.Error(), "empty rows") {
		t.Errorf("batch empty rows: got %v", err)
	}
	bad := db.Insert("t", 42) // 非 struct/map
	if bad.Error() == nil {
		t.Error("insert on int: want builder error")
	}
	if bad.Add("AND 1=1").Error() == nil {
		t.Error("error must propagate through subsequent Add")
	}
	if db.RegisterMacro('#', "bind").Error() == nil {
		t.Error("reserved macro prefix '#': want error")
	}
	if db.RegisterMacro('\t', "bind").Error() == nil {
		t.Error("non-printable macro prefix: want error")
	}
	if db.RegisterPipe("", nil).Error() == nil {
		t.Error("empty pipe name: want error")
	}
}

// Bind 的 *Node nil 守卫: nil 指针 → SQL NULL, 不 panic。
func TestBindNilNodePointer(t *testing.T) {
	db := newTestSQL()
	checkSQL(t, "nil *Node binds as NULL",
		db.Add("SELECT #{1}", (*Node)(nil)),
		"SELECT $1", nil)
}
