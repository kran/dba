package dba

import (
	"fmt"
)

// itemType 词法 token 类型。
type itemType int

const (
	itemError itemType = iota // 错误 (值=错误信息)
	itemText                  // 普通文本 (值=原文)
	itemParam                 // #{...} 参数绑定 (值=key/序号)
	itemVar                   // ${...} 变量引用 (值=变量名)
	itemIdent                 // @{...} 标识符引用 (值=key)
	itemRaw                   // !{...} 原始文本 (值=key)
)

// item 词法 token。
type item struct {
	typ itemType
	pos int    // 起始位置 (错误定位)
	val string // 文本原文或宏内容
}

// stateFn 词法状态函数: 返回下一个状态, nil 结束。
// 状态机模式对齐 Go 标准库 text/template/parse/lex.go (Rob Pike lexer)。
type stateFn func(*lexer) stateFn

// lexer 宏模板扫描器。
//
// 词法规则:
//   - 单引号/双引号/反引号字面量整体跳过 (引号内 { } 不参与宏解析),
//     支持 \x 转义与 ” 双写引号; 引号逻辑单点实现 (lexQuote), 正文与宏内容共用
//   - 双写转义: ##{ → 字面 #{ (四个前缀通用)
//   - 宏内容以第一个"引号外"的 } 结束
type lexer struct {
	input  string
	pos    int     // 当前扫描位置
	start  int     // 当前 token 起点
	prefix byte    // 当前宏前缀 (# $ @ !), lexMacro 期间有效
	prev   stateFn // lexQuote 闭合后返回的外层状态
	items  []item
}

// lex 扫描模板, 返回 token 列表; 词法错误以 error 返回。
func lex(input string) ([]item, error) {
	l := &lexer{input: input}
	for state := stateFn(lexText); state != nil; {
		state = state(l)
	}
	if n := len(l.items); n > 0 && l.items[n-1].typ == itemError {
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
func (l *lexer) emit(typ itemType) {
	if l.pos > l.start {
		l.items = append(l.items, item{typ, l.start, l.input[l.start:l.pos]})
	}
	l.start = l.pos
}

// emitRange 提交指定区间为 token (双写转义需要), start 前进到 end。
func (l *lexer) emitRange(typ itemType, start, end int) {
	if end > start {
		l.items = append(l.items, item{typ, start, l.input[start:end]})
	}
	l.start = end
}

func (l *lexer) errorf(format string, args ...any) stateFn {
	l.items = append(l.items, item{itemError, l.start, fmt.Sprintf(format, args...)})
	return nil
}

// ── 状态函数 ─────────────────────────────────────────────

// macroPrefix 前缀字符 → token 类型; 未知前缀返回 ok=false (按普通文本处理)。
func macroPrefix(prefix byte) (itemType, bool) {
	switch prefix {
	case '#':
		return itemParam, true
	case '$':
		return itemVar, true
	case '@':
		return itemIdent, true
	case '!':
		return itemRaw, true
	}
	return 0, false
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
			if _, ok := macroPrefix(prefix); !ok {
				l.next()
				continue
			}
			// 双写转义: ##{ → 字面 #{ (文本区间去掉一个前缀字符)
			if l.pos >= 2 && l.input[l.pos-2] == prefix {
				l.emitRange(itemText, l.start, l.pos-2)
				l.items = append(l.items, item{itemText, l.pos - 2, string(prefix) + "{"})
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
			kind, _ := macroPrefix(l.prefix)
			// start 指向 { 位置, 内容 = (start, pos) 之间
			l.items = append(l.items, item{kind, l.start, l.input[l.start+1 : l.pos]})
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

// lexQuote 引号字面量: 跳过直到闭合引号 (支持 \x 转义与 ” 双写), 闭合后回外层状态。
func lexQuote(l *lexer) stateFn {
	quote := l.input[l.pos-1] // 已消费的开引号
	for l.pos < len(l.input) {
		switch c := l.input[l.pos]; c {
		case '\\':
			l.next()
			if l.pos < len(l.input) {
				l.next()
			}
		case quote:
			l.next() // 消费闭引号
			// '' 双写转义: 立即跟同引号 → 继续
			if l.pos < len(l.input) && l.input[l.pos] == quote {
				l.next()
				continue
			}
			return l.prev
		default:
			l.next()
		}
	}
	return l.errorf("unclosed quote")
}
