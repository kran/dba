package dba_test

import (
	"strings"
	"testing"

	"github.com/kran/dba"
)

// ── 管道注册 ──

func TestRegisterPipe(t *testing.T) {
	q, _ := newQ(t)
	q = q.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
		v, err := ctx.Resolve(content)
		if err != nil {
			return err
		}
		ctx.AddParam(strings.ToUpper(v.(string)))
		return nil
	})
	args := toSQL(t, q.Add("WHERE name = #{1|upper}", "bob"), "WHERE name = $1")
	if len(args) != 1 || args[0] != "BOB" {
		t.Errorf("args: %v", args)
	}
}

// 注册管道不影响原 builder (copy-on-write 隔离)
func TestRegisterPipe_Isolation(t *testing.T) {
	q, _ := newQ(t)
	q2 := q.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
		v, err := ctx.Resolve(content)
		if err != nil {
			return err
		}
		ctx.AddParam(strings.ToUpper(v.(string)))
		return nil
	})
	toSQL(t, q2.Add("WHERE name = #{1|upper}", "bob"), "WHERE name = $1")
	// 原 builder 无 upper 管道 → 报错
	if _, _, err := q.Add("WHERE name = #{1|upper}", "bob").ToSQL(); err == nil {
		t.Fatal("expected unknown pipe error on original builder")
	}
}

// 未知管道报错
func TestUnknownPipe(t *testing.T) {
	q, _ := newQ(t)
	if _, _, err := q.Add("WHERE x = #{1|nope}", 1).ToSQL(); err == nil {
		t.Fatal("expected unknown pipe error")
	}
}

// ── 宏注册 ──

func TestRegisterMacro(t *testing.T) {
	q, _ := newQ(t)
	q = q.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
		v, err := ctx.Resolve(content)
		if err != nil {
			return err
		}
		ctx.AddParam(strings.ToUpper(v.(string)))
		return nil
	}).RegisterMacro('^', "upper")
	args := toSQL(t, q.Add("WHERE name = ^{1}", "bob"), "WHERE name = $1")
	if len(args) != 1 || args[0] != "BOB" {
		t.Errorf("args: %v", args)
	}
}

// 保留前缀不可注册
func TestRegisterMacro_ReservedPrefix(t *testing.T) {
	q, _ := newQ(t)
	if _, _, err := q.RegisterMacro('#', "expand").ToSQL(); err == nil {
		t.Fatal("expected error for reserved prefix #")
	}
	if _, _, err := q.RegisterMacro('$', "var").ToSQL(); err == nil {
		t.Fatal("expected error for reserved prefix $")
	}
}

// 宏指向未知管道: 渲染时报错 (注册时宽松)
func TestRegisterMacro_UnknownPipe(t *testing.T) {
	q, _ := newQ(t)
	q = q.RegisterMacro('%', "nope")
	if _, _, err := q.Add("WHERE x = %{1}", 1).ToSQL(); err == nil {
		t.Fatal("expected error for macro referring to unknown pipe")
	}
}

// 自定义宏的双写转义
func TestRegisterMacro_Escape(t *testing.T) {
	q, _ := newQ(t)
	q = q.RegisterPipe("upper", func(ctx dba.RenderCtx, content string) error {
		v, err := ctx.Resolve(content)
		if err != nil {
			return err
		}
		ctx.AddParam(strings.ToUpper(v.(string)))
		return nil
	}).RegisterMacro('^', "upper")
	toSQL(t, q.Add("SELECT ^^{1}"), "SELECT ^{1}")
}

// 宏与内置管道等价: @ ≡ #{1|quote}
func TestMacroAliasEquivalence(t *testing.T) {
	q, _ := newQ(t)
	// @ = 字面量 lident: @{name} ≡ #{name|literalquote}
	toSQL(t, q.Add("SELECT @{name}"), `SELECT "name"`)
	toSQL(t, q.Add("SELECT #{name|literalquote}"), `SELECT "name"`)
}

// ── copy-on-write 隔离 (Var/Use 写不影响原实例) ──

func TestVarCopyOnWriteIsolation(t *testing.T) {
	q, _ := newQ(t)
	q2 := q.Var("cond", "WHERE a = 1")
	// 原实例无 cond 变量 → 报错
	if _, _, err := q.Add("SELECT 1 ${cond}").ToSQL(); err == nil {
		t.Fatal("expected undefined variable on original builder")
	}
	// 新实例正常
	toSQL(t, q2.Add("SELECT 1 ${cond}"), "SELECT 1 WHERE a = 1")
}

func TestUseCopyOnWriteIsolation(t *testing.T) {
	q, _ := newQ(t)
	q2 := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return next
	})
	// hooks 隔离: 原实例无 hook 也能正常执行 (编译/运行不炸即可)
	if _, err := q.Add("SELECT 1").Get(&struct{}{}); err != nil {
		t.Logf("get err: %v", err) // sqlite 无表, 报错正常; 只要不 panic
	}
	// 新实例有 hook 且正常 (ToSQL 不炸)
	if _, _, err := q2.Add("SELECT 1").ToSQL(); err != nil {
		t.Fatalf("toSQL: %v", err)
	}
}

// 管道语法容忍空白: #{1| expand } 与 #{1|expand} 等价 (与 $ 变量一致)
func TestPipeWhitespaceTolerant(t *testing.T) {
	q, _ := newQ(t)
	args := toSQL(t, q.Add("WHERE id IN (#{1| expand })", []int{1, 2}), "WHERE id IN ($1, $2)")
	if len(args) != 2 {
		t.Errorf("args: %v", args)
	}
	toSQL(t, q.Add("WHERE id = #{ 1 }", 5), "WHERE id = $1")
}
