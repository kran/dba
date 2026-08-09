package dba

import (
	"fmt"
)

// itemKind 词法 token 类型。
type itemKind int

const (
	itemText  itemKind = iota // 普通文本 (值=原文)
	itemMacro                 // 宏 (值=内容, prefix=前缀字符)
	itemError                 // 错误 (值=错误信息)
)

// item 词法 token。
type item struct {
	kind   itemKind
	prefix byte   // 宏前缀 (# $ @ ! % ...), 仅 itemMacro
	pos    int    // 起始位置 (错误定位)
	val    string // 文本原文或宏内容
}

// stateFn 词法状态函数: 返回下一个状态, nil 结束。
// 状态机模式对齐 Go 标准库 text/template/parse/lex.go (Rob Pike lexer)。
type stateFn func(*lexer) stateFn

// lexer 宏模板扫描器。
//
// 词法规则:
//   - 单引号/双引号/反引号字面量整体跳过 (引号内 { } 不参与宏解析),
//     支持 \x 转义与 ” 双写引号; 引号逻辑单点实现 (lexQuote), 正文与宏内容共用
//   - 双写转义: XX{ → 字面 X{ (X 为宏前缀, 由 macros 表驱动)
//   - 宏内容以第一个"引号外"的 } 结束
type lexer struct {
	input  string
	pos    int             // 当前扫描位置
	start  int             // 当前 token 起点
	prefix byte            // 当前宏前缀, lexMacro 期间有效
	prev   stateFn         // lexQuote 闭合后返回的外层状态
	macros map[byte]string // 宏前缀表: 前缀 → 管道名 (仅用于识别)
	items  []item
}

// lex 扫描模板, 返回 token 列表; 词法错误以 error 返回。
// macros 为宏前缀表 ('#'/'$' 为保留前缀, 恒为宏; 其余查表)。
func lex(input string, macros map[byte]string) ([]item, error) {
	l := &lexer{input: input, macros: macros}
	for state := stateFn(lexText); state != nil; {
		state = state(l)
	}
	if n := len(l.items); n > 0 && l.items[n-1].kind == itemError {
		return nil, fmt.Errorf("%s", l.items[n-1].val)
	}
	return l.items, nil
}

// ── 游标原语 ─────────────────────────────────────────────

func (l *lexer) next() byte {
	c := l.input[l.pos]
	l.pos++
	return c
}

// emit 提交 start..pos 为 token (零拷贝切片), start 前进。
func (l *lexer) emit(kind itemKind) {
	if l.pos > l.start {
		l.items = append(l.items, item{kind: kind, pos: l.start, val: l.input[l.start:l.pos]})
	}
	l.start = l.pos
}

// emitRange 提交指定区间为 token (双写转义需要), start 前进到 end。
func (l *lexer) emitRange(kind itemKind, start, end int) {
	if end > start {
		l.items = append(l.items, item{kind: kind, pos: start, val: l.input[start:end]})
	}
	l.start = end
}

func (l *lexer) errorf(format string, args ...any) stateFn {
	l.items = append(l.items, item{kind: itemError, pos: l.start, val: fmt.Sprintf(format, args...)})
	return nil
}

// ── 状态函数 ─────────────────────────────────────────────

// isMacroPrefix 判断字符是否为宏前缀: '#'/'$' 保留, 其余查注册宏表。
func isMacroPrefix(prefix byte, macros map[byte]string) bool {
	if prefix == '#' || prefix == '$' {
		return true
	}
	_, ok := macros[prefix]
	return ok
}

// lexText 普通文本: 遇引号 → lexQuote, 遇宏前缀+{ → lexMacro, EOF 收尾。
func lexText(l *lexer) stateFn {
	for l.pos < len(l.input) {
		switch c := l.input[l.pos]; c {
		case '\'', '"', '`':
			l.next() // 消费开引号
			l.prev = lexText
			return lexQuote

		case '{':
			if l.pos == 0 {
				l.next()
				continue
			}
			prefix := l.input[l.pos-1]
			if !isMacroPrefix(prefix, l.macros) {
				l.next()
				continue
			}
			// 双写转义: XX{ → 字面 X{ (文本区间去掉一个前缀字符)
			if l.pos >= 2 && l.input[l.pos-2] == prefix {
				l.emitRange(itemText, l.start, l.pos-2)
				l.items = append(l.items, item{kind: itemText, pos: l.pos - 2, val: string(prefix) + "{"})
				l.pos++ // 消费 {
				l.start = l.pos
				continue
			}
			// 宏开始: 提交前缀字符之前的文本 (前缀本身不输出), 进入宏内容
			l.emitRange(itemText, l.start, l.pos-1)
			l.start = l.pos // 宏 token 起点指向 { (content = start+1 .. })
			l.next()        // 消费 {
			l.prefix = prefix
			l.prev = lexText
			return lexMacro

		default:
			l.next()
		}
	}
	l.emit(itemText)
	return nil
}

// lexMacro 宏内容: 遇引号 → lexQuote, 遇引号外 } 结束宏。
func lexMacro(l *lexer) stateFn {
	for l.pos < len(l.input) {
		switch c := l.input[l.pos]; c {
		case '\'', '"', '`':
			l.next()
			l.prev = lexMacro
			return lexQuote

		case '}':
			// start 指向 { 位置, 内容 = (start, pos) 之间
			l.items = append(l.items, item{kind: itemMacro, prefix: l.prefix, pos: l.start, val: l.input[l.start+1 : l.pos]})
			l.pos++ // 消费 }
			l.start = l.pos
			l.prefix = 0
			return lexText

		default:
			l.next()
		}
	}
	return l.errorf("unclosed macro %c{", l.prefix)
}

// lexQuote 引号字面量: 跳过直到闭合引号, ” 双写视为续接。
// 刻意不处理反斜杠转义 —— lexer 只找字面量边界 (防引号内的 #{ 被
// 误认为宏), 不解释内容; 转义语义 (MySQL \x / PG E” / sql_mode)
// 归数据库, lexer 按标准 SQL 的边界规则扫描: 引号结束于下一个
// 非双写的同引号。
func lexQuote(l *lexer) stateFn {
	quote := l.input[l.pos-1]
	for l.pos < len(l.input) {
		if l.next() == quote {
			if l.pos < len(l.input) && l.input[l.pos] == quote {
				l.next() // '' 双写: 续接
				continue
			}
			return l.prev
		}
	}
	return l.errorf("unclosed quote")
}
