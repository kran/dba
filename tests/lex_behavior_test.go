package dba_test

import (
	"strings"
	"testing"

	"github.com/kran/dba"
)

// ── 词法器行为测试 (通过公开 API 验证 lex 结果) ──

func toSQL(t *testing.T, q *dba.SQL, want string) []any {
	t.Helper()
	sql, args, err := q.ToSQL()
	if err != nil {
		t.Fatalf("ToSQL: %v", err)
	}
	if sql != want {
		t.Errorf("sql:\n got  %q\n want %q", sql, want)
	}
	return args
}

// 基本宏扫描
func TestLexerBasic(t *testing.T) {
	q, _ := newQ(t)
	args := toSQL(t, q.Add("SELECT #{1|quote} FROM !{2} WHERE id = #{3}", "name", "users", 1),
		`SELECT "name" FROM users WHERE id = $1`)
	if len(args) != 1 || args[0] != 1 {
		t.Errorf("args: %v", args)
	}
}

// 双写转义: 四种前缀
func TestLexerDoubleEscape(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"SELECT ##{1}", "SELECT #{1}"},
		{"SELECT $${x}", "SELECT ${x}"},
		{"SELECT @@{c}", "SELECT @{c}"},
		{"SELECT !!{t}", "SELECT !{t}"},
		{"SELECT ###{1}", "SELECT ##{1}"}, // 三连 #: 转义一组 + 剩余字面
	} {
		q, _ := newQ(t)
		toSQL(t, q.Add(tc.in), tc.want)
	}
}

// 引号字面量: 引号内的 {} 不参与宏解析; 引号内 ##{ 不转义
func TestLexerQuotedBraces(t *testing.T) {
	q, _ := newQ(t)
	toSQL(t, q.Add("WHERE a = '{1}' AND b = #{1}", 42), `WHERE a = '{1}' AND b = $1`)
	q2, _ := newQ(t)
	toSQL(t, q2.Add("WHERE a = '##{1}'"), `WHERE a = '##{1}'`)
}

// 引号: \x 转义与 ” 双写; 反引号
func TestLexerQuoteEscapes(t *testing.T) {
	//q, _ := newQ(t)
	//toSQL(t, q.Add("WHERE a = 'it\\'s' AND b = #{1}", 1), `WHERE a = 'it\'s' AND b = $1`)
	q2, _ := newQ(t)
	toSQL(t, q2.Add("WHERE a = 'it''s' AND b = #{1}", 1), `WHERE a = 'it''s' AND b = $1`)
	q3, _ := newQ(t)
	toSQL(t, q3.Add("`a}b` = #{1}", 1), "`a}b` = $1")
}

// 宏内容含引号 } 不截断: 内容完整 → 错误信息包含完整内容
func TestLexerMacroWithQuotedBrace(t *testing.T) {
	q, _ := newQ(t)
	_, _, err := q.Add("SELECT !{REPLACE(name, '}', '')}", map[string]any{"col": "x"}).ToSQL()
	if err == nil || !strings.Contains(err.Error(), "REPLACE(name, '}', '')") {
		t.Errorf("expected error mentioning full macro content, got %v", err)
	}
}

// 错误: 未闭合宏 / 未闭合引号
func TestLexerErrors(t *testing.T) {
	q, _ := newQ(t)
	if _, _, err := q.Add("SELECT !{abc").ToSQL(); err == nil {
		t.Fatal("expected unclosed macro error")
	}
	q2, _ := newQ(t)
	if _, _, err := q2.Add("SELECT 'abc").ToSQL(); err == nil {
		t.Fatal("expected unclosed quote error")
	}
}

// 边界: 相邻宏 / 模板以 { 开头 / 空宏内容 / 宏内容裸 { / 转义后接宏
func TestLexerExtras(t *testing.T) {
	q, _ := newQ(t)
	toSQL(t, q.Add("#{1}#{2}", 1, 2), "$1$2")

	q2, _ := newQ(t)
	toSQL(t, q2.Add("{1} #{1}", 2), "{1} $1")

	q3, _ := newQ(t)
	if _, _, err := q3.Add("SELECT #{}", 1).ToSQL(); err == nil {
		t.Fatal("expected no args error for empty macro content")
	}

	q4, _ := newQ(t)
	if _, _, err := q4.Add("SELECT ${a{b}}").ToSQL(); err == nil {
		t.Fatal("expected undefined variable error for bare { in macro content")
	}

	q5, _ := newQ(t)
	toSQL(t, q5.Add("##{1} #{1}", 2), "#{1} $1")
}

// $ 变量: 注册内容含引号 } 正常
func TestLexerVarWithQuotedBrace(t *testing.T) {
	q, _ := newQ(t)
	q = q.Var("cond", "WHERE a = '}'")
	toSQL(t, q.Add("SELECT 1 ${cond}"), "SELECT 1 WHERE a = '}'")
}
