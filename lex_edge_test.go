package dba

// lex_edge_test.go — 词法器边界测试 (package dba 内部, 直接断言 token 流)。
//
// 黄金测试覆盖渲染的成功路径; 这里钉 lexer 本身的错误路径与边界:
// 未闭合宏/引号、EOF 截断、引号内 } 不闭合、转义组合、空输入、错误消息内容。

import (
	"strings"
	"testing"
)

// lexToks 直接扫描并返回 token 流 (失败即 t.Fatal)。
func lexToks(t *testing.T, input string) []item {
	t.Helper()
	items, err := lex(input, defaultMacros)
	if err != nil {
		t.Fatalf("lex(%q): unexpected error: %v", input, err)
	}
	return items
}

// lexErr 扫描并断言错误消息包含子串。
func lexErr(t *testing.T, input, substr string) {
	t.Helper()
	_, err := lex(input, defaultMacros)
	if err == nil {
		t.Fatalf("lex(%q): want error containing %q, got none", input, substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("lex(%q): want error containing %q, got: %v", input, substr, err)
	}
}

func TestLexEdge_EmptyInput(t *testing.T) {
	items := lexToks(t, "")
	if len(items) != 0 {
		t.Fatalf("empty input should yield no tokens: %v", items)
	}
}

func TestLexEdge_PlainText(t *testing.T) {
	items := lexToks(t, "SELECT * FROM t WHERE x = 1")
	if len(items) != 1 || items[0].kind != itemText || items[0].val != "SELECT * FROM t WHERE x = 1" {
		t.Fatalf("plain text should be one text token: %v", items)
	}
}

func TestLexEdge_MacroTokenShape(t *testing.T) {
	items := lexToks(t, "a#{1}b")
	if len(items) != 3 {
		t.Fatalf("expect text/macro/text: %v", items)
	}
	if items[0].kind != itemText || items[0].val != "a" || items[0].pos != 0 {
		t.Fatalf("text token: %+v", items[0])
	}
	// 宏 token 的 pos 指向 { (词法注释: "宏 token 起点指向 {")
	if items[1].kind != itemMacro || items[1].prefix != '#' || items[1].val != "1" || items[1].pos != 2 {
		t.Fatalf("macro token: %+v", items[1])
	}
	if items[2].kind != itemText || items[2].val != "b" {
		t.Fatalf("text token: %+v", items[2])
	}
}

// 未闭合宏: 报错含前缀字符。
func TestLexEdge_UnclosedMacro(t *testing.T) {
	lexErr(t, "SELECT #{1", "unclosed macro #{")
	lexErr(t, "SELECT ${a", "unclosed macro ${")
	lexErr(t, "SELECT @{x", "unclosed macro @{")
}

// EOF 截断: 前缀后直接结束。
func TestLexEdge_TruncatedAtEOF(t *testing.T) {
	lexErr(t, "SELECT #{", "unclosed macro #{")
	// 单独 # 无 { 不构成宏 — 普通文本
	items := lexToks(t, "SELECT #")
	if len(items) != 1 || items[0].kind != itemText {
		t.Fatalf("bare # should be text: %v", items)
	}
}

// 未闭合引号 (正文 + 宏内容, 三种引号)。
func TestLexEdge_UnclosedQuote(t *testing.T) {
	lexErr(t, "SELECT 'abc", "unclosed quote")
	lexErr(t, `SELECT "abc`, "unclosed quote")
	lexErr(t, "SELECT `abc", "unclosed quote")
	lexErr(t, "SELECT #{'abc}", "unclosed quote")
	lexErr(t, "SELECT #{1} 'abc", "unclosed quote")
}

// 引号内 } 不闭合宏 (宏内容 + 变量内容)。
func TestLexEdge_QuoteBraceNotClosing(t *testing.T) {
	items := lexToks(t, "#{'}'}")
	if len(items) != 1 || items[0].kind != itemMacro || items[0].val != "'}'" {
		t.Fatalf("quote brace should not close macro: %v", items)
	}
	items = lexToks(t, `#{'a"b`+"`"+`c}'}`)
	if len(items) != 1 || items[0].kind != itemMacro {
		t.Fatalf("mixed quotes inside macro: %v", items)
	}
}

// 宏内容里的裸 } 结束宏, 后面的 } 是文本。
func TestLexEdge_BareBraceAfterMacro(t *testing.T) {
	items := lexToks(t, "#{1}}")
	if len(items) != 2 || items[0].kind != itemMacro || items[1].kind != itemText || items[1].val != "}" {
		t.Fatalf("bare brace after macro should be text: %v", items)
	}
}

// 双写转义组合: ##{ → 字面 #{ (token 切分 = "#{" + 后续文本, 拼接后是字面量)。
func TestLexEdge_EscapeCombos(t *testing.T) {
	items := lexToks(t, "##{1}")
	// 拼接全部文本 token = 字面 "#{1}" (转义 "#{" + 文本 "1}")
	var joined string
	for _, it := range items {
		if it.kind != itemText {
			t.Fatalf("escaped sequence should be all text: %v", items)
		}
		joined += it.val
	}
	if joined != "#{1}" {
		t.Fatalf("escaped join = %q, want #{1}", joined)
	}
	// 转义后接真宏: 渲染层验证 (转义 "#{1}" 字面 + 真宏绑定)
	db := newTestSQL()
	got, args, err := db.Add("##{1} #{1}", 42).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if got != "#{1} $1" || len(args) != 1 || args[0] != 42 {
		t.Fatalf("escape then macro: %q %v", got, args)
	}
}

// 引号内的宏前缀不解析 (字符串里的 #{ 是文本)。
func TestLexEdge_MacroPrefixInQuote(t *testing.T) {
	items := lexToks(t, "'#{1}'")
	if len(items) != 1 || items[0].kind != itemText || items[0].val != "'#{1}'" {
		t.Fatalf("macro prefix inside quote should be text: %v", items)
	}
}

// 双写引号 (” → 字面 ') 与 \x 转义。
func TestLexEdge_QuoteDoublingAndEscape(t *testing.T) {
	items := lexToks(t, `'it''s'`)
	if len(items) != 1 || items[0].kind != itemText || items[0].val != `'it''s'` {
		t.Fatalf("doubled quote: %v", items)
	}
	items = lexToks(t, `'a\'b'`)
	if len(items) != 1 || items[0].kind != itemText {
		t.Fatalf("escaped quote: %v", items)
	}
}

// 相邻宏 (无间隔文本)。
func TestLexEdge_AdjacentMacros(t *testing.T) {
	items := lexToks(t, "#{1}#{2}")
	if len(items) != 2 || items[0].prefix != '#' || items[1].prefix != '#' {
		t.Fatalf("adjacent macros: %v", items)
	}
}

// $ 保留前缀恒为宏 (不查表); 未注册前缀是文本。
func TestLexEdge_PrefixRecognition(t *testing.T) {
	items := lexToks(t, "${a}")
	if len(items) != 1 || items[0].kind != itemMacro || items[0].prefix != '$' {
		t.Fatalf("$ is reserved macro prefix: %v", items)
	}
	// 未注册前缀 + { → 文本
	items = lexToks(t, "%{a}")
	if len(items) != 1 || items[0].kind != itemText {
		t.Fatalf("unregistered prefix should be text: %v", items)
	}
}

// 错误 token 的位置信息 (itemError.pos)。
func TestLexEdge_ErrorPosition(t *testing.T) {
	l := &lexer{input: "abc#{1", macros: defaultMacros}
	for state := stateFn(lexText); state != nil; {
		state = state(l)
	}
	if len(l.items) == 0 || l.items[len(l.items)-1].kind != itemError {
		t.Fatalf("want trailing itemError: %v", l.items)
	}
	// 未闭合宏的 pos 指向 { 位置 (abc=0-2, #=3, {=4)
	if pos := l.items[len(l.items)-1].pos; pos != 4 {
		t.Fatalf("error pos = %d, want 4", pos)
	}
}

// 不同引号混合: 只有同类型引号闭合 — 不同引号是引号状态内的普通字符,
// 任意引号状态内宏一律不解析 (lexQuote 单点实现的自然结果)。
func TestLexEdge_QuoteMixing(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // 期望渲染 (宏不解析 → 原样); 空 = 期望 unclosed quote 错误
	}{
		{"single then double", `'#{1}"`, ""},         // 单引号开, 双引号是文本, 无闭合 → 错误
		{"double then single", `"#{1}'"`, `"#{1}'"`}, // 双引号开, 单引号是文本, 双引号闭合 → 宏不解析
		{"mixed inside", `'a"b'`, `'a"b'`},           // 单引号内双引号是文本
		{"nested quotes", `"'#{1}'"`, `"'#{1}'"`},    // 双引号内单引号是文本 → 宏不解析
		{"single with double close", `'a"`, ""},      // 单引号开, 无单引号闭合 → 错误
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := lex(c.input, defaultMacros)
			if c.want == "" {
				if err == nil {
					t.Fatalf("want unclosed quote error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var joined string
			for _, it := range got {
				if it.kind != itemText {
					t.Fatalf("macro should not parse inside quotes: %v", got)
				}
				joined += it.val
			}
			if joined != c.want {
				t.Fatalf("joined = %q, want %q", joined, c.want)
			}
		})
	}
}
