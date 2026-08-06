package dba_test

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kran/dba"
)

// ── 并发安全 (配合 go test -race 验证 copy-on-write 无竞争) ──

func TestConcurrentBuilders(t *testing.T) {
	base, _ := newQ(t)
	base = base.Var("where", "WHERE status = #{1}", "active")
	base = base.Add("SELECT * FROM users ${where} AND id IN (#{1|expand})", []int{1, 2})

	var wg sync.WaitGroup

	// 只读并发: 同一 base 派生 + ToSQL (共享 pipes/macros/varNodes 只读)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			q := base.Add("AND age > #{1}", i)
			sql, args, err := q.ToSQL()
			if err != nil {
				t.Errorf("toSQL: %v", err)
				return
			}
			if !strings.Contains(sql, "WHERE status = $1 AND id IN ($2, $3)") {
				t.Errorf("sql mismatch: %q", sql)
			}
			if len(args) != 4 {
				t.Errorf("args: %v", args)
			}
		}(i)
	}

	// 只读并发: 同一实例直接 ToSQL
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := base.ToSQL(); err != nil {
				t.Errorf("toSQL on shared base: %v", err)
			}
		}()
	}

	// 写并发: RegisterPipe 写时复制 (不影响共享 base)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "p" + strconv.Itoa(i)
			q := base.RegisterPipe(name, func(ctx dba.RenderCtx, content string) error {
				v, err := ctx.Resolve(content)
				if err != nil {
					return err
				}
				ctx.Bind(v)
				return nil
			})
			if _, _, err := q.Add("SELECT #{1|"+name+"}", i).ToSQL(); err != nil {
				t.Errorf("register+toSQL: %v", err)
			}
		}(i)
	}
	wg.Wait()
}

// ── 占位符/参数守恒不变量: 任意模板渲染后 SQL 占位符数 == finalArgs 数 ──

// countPlaceholders 数 SQL 中的占位符 ($N, 跳过引号字面量)。
func countPlaceholders(s string) int {
	n := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '$':
			if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
				n++
			}
		}
	}
	return n
}

func TestPlaceholderConservation(t *testing.T) {
	base, _ := newQ(t)
	base = base.Var("cond", "status = #{1} AND type = #{2}", "active", 3)

	cases := []struct {
		name string
		q    *dba.SQL
	}{
		{"expand + 重复引用", base.Add("SELECT * FROM users WHERE id IN (#{1|expand}) AND s = #{1} AND c = #{2}", []int{1, 2}, "x", 5)},
		{"变量递归", base.Add("SELECT * FROM users WHERE ${cond} AND id = #{1}", 9)},
		{"转义 + 宏 + 管道", base.Add("SELECT ##{1} AS lit, name = #{1|raw}, id IN (#{1|expand})", []int{7, 8}, "bob")},
		{"引号含 ?", base.Add("SELECT * FROM t WHERE a = '?' AND b = #{1}", 1)},
		{"纯文本", base.Add("SELECT * FROM users")},
		{"嵌套变量", base.Var("outer", "${cond} AND age > #{1}", 21).Add("SELECT * FROM users WHERE ${outer}")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := tc.q.ToSQL()
			if err != nil {
				t.Fatalf("toSQL: %v", err)
			}
			if got := countPlaceholders(sql); got != len(args) {
				t.Fatalf("placeholder count %d != args count %d\nsql: %s", got, len(args), sql)
			}
		})
	}
}

// ── 三层嵌套: Var 内容含管道 + 宏别名 ──

func TestNestedVarWithPipesAndMacros(t *testing.T) {
	q, _ := newQ(t)
	// Var 注册时携带参数, 内容含 #{1|raw} 管道 + @{2} 宏别名
	q = q.Var("cond", "status = #{1|raw} AND name = #{2|quote}", "active", "users")

	// 变量递归渲染 + 外层 Add 参数共存
	sql, args, err := q.Add("SELECT * FROM users WHERE ${cond} AND id = #{1}", 5).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	want := `SELECT * FROM users WHERE status = active AND name = "users" AND id = $1`
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	// 参数顺序: varNode.Args 先, Add 参数后
	if len(args) != 1 || args[0] != 5 {
		t.Errorf("args: %v", args)
	}
}

// ── 自定义管道中途返回 error: build 中止 ──

func TestPipeMidwayError(t *testing.T) {
	q, _ := newQ(t)
	q = q.RegisterPipe("boom", func(ctx dba.RenderCtx, content string) error {
		ctx.Bind(content)                      // 先写占位符 (字面量)
		return fmt.Errorf("boom: pipe failed") // 再报错
	})
	_, _, err := q.Add("SELECT #{1|boom} FROM t", 1).ToSQL()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}
