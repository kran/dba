package dba

import (
	"fmt"
	"maps"
	"reflect"
	"strings"
)

// RenderCtx 渲染上下文: 管道与宏的渲染出口。
type RenderCtx interface {
	// AddParam 写入一个占位符并收集绑定参数 (内部维护序号与方言格式)。
	AddParam(v any)
	// WriteString 写入原始 SQL 文本。
	WriteString(s string)
	// QuoteIdent 写入方言 quoting 的标识符。
	QuoteIdent(s string)
}

// Pipe 管道: 值 → SQL 渲染。
//
// 模板语法 #{key|pipe} 中的 pipe 名查注册表调用; 宏 (如 @/!) 是管道的语法别名。
// 内置: bind(默认)/expand/raw/ident; 用户通过 RegisterPipe 注册。
type Pipe func(ctx RenderCtx, v any) error

// ── 内置管道 ─────────────────────────────────────────────

func pipeBind(ctx RenderCtx, v any) error {
	ctx.AddParam(v)
	return nil
}

// pipeExpand 展开 slice/array 为独立占位符:
//
//	q.Add("WHERE id IN (#{1|expand})", []int{1, 2}) → IN ($1, $2)
//
// 空切片展开为 0 个参数 (如 `IN ()`), 由数据库报语法错误, 框架不拦截。
// []byte 同样逐字节展开 (显式意图自担)。
func pipeExpand(ctx RenderCtx, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return fmt.Errorf("dba: expand pipe requires slice or array, got %T", v)
	}
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			ctx.WriteString(", ")
		}
		ctx.AddParam(rv.Index(i).Interface())
	}
	return nil
}

// pipeRaw 原始文本注入 (调用者负责安全):
//
//	q.Add("WHERE created_at > #{1|raw}", "NOW()")
func pipeRaw(ctx RenderCtx, v any) error {
	if v == nil {
		return fmt.Errorf("dba: raw pipe requires non-nil value")
	}
	ctx.WriteString(fmt.Sprintf("%v", v))
	return nil
}

// pipeIdent 方言 quoting 标识符:
//
//	q.Add("SELECT #{1|ident} FROM t", "name")
func pipeIdent(ctx RenderCtx, v any) error {
	if v == nil {
		return fmt.Errorf("dba: ident pipe requires non-nil value")
	}
	ctx.QuoteIdent(fmt.Sprintf("%v", v))
	return nil
}

// defaultPipes 内置管道集 (New 时初始化到实例)。
var defaultPipes = map[string]Pipe{
	"bind":   pipeBind,
	"expand": pipeExpand,
	"raw":    pipeRaw,
	"ident":  pipeIdent,
}

// defaultMacros 内置宏别名 (前缀 → 管道名)。'#'/'$' 为保留前缀, 不进表。
var defaultMacros = map[byte]string{
	'@': "ident", // @{1} ≡ #{1|ident}
	'!': "raw",   // !{1} ≡ #{1|raw}
}

// ── 注册 API (copy-on-write, 无并发问题) ─────────────────

// RegisterPipe 注册自定义管道, 返回新 builder:
//
//	q := db.RegisterPipe("upper", func(ctx RenderCtx, v any) error {
//		ctx.AddParam(strings.ToUpper(fmt.Sprint(v)))
//		return nil
//	})
//	q.Add("WHERE name = #{1|upper}", "bob") → $1 = "BOB"
func (d *SQL) RegisterPipe(name string, fn Pipe) *SQL {
	clone := d.copy()
	if name == "" {
		clone.err = fmt.Errorf("dba: pipe name must not be empty")
		return clone
	}
	clone.pipes = maps.Clone(d.pipes)
	clone.pipes[name] = fn
	return clone
}

// RegisterMacro 注册宏别名 (前缀 → 管道名), 返回新 builder:
//
//	q := db.RegisterMacro('^', "upper")
//	q.Add("WHERE name = ^{1}", "bob") → 等价 #{1|upper}
//
// '#'/'$' 为保留前缀不可注册; 管道名允许指向尚未注册的管道 (渲染时报错)。
func (d *SQL) RegisterMacro(prefix byte, pipe string) *SQL {
	clone := d.copy()
	clone.macros = maps.Clone(d.macros)
	switch {
	case prefix == '#' || prefix == '$':
		clone.err = fmt.Errorf("dba: macro prefix %c is reserved", prefix)
	case prefix < 33 || prefix > 126:
		clone.err = fmt.Errorf("dba: macro prefix must be printable ASCII")
	default:
		clone.macros[prefix] = pipe
	}
	return clone
}

// buildRenderCtx build 侧 RenderCtx 实现。
type buildRenderCtx struct {
	sqlBuilder *strings.Builder
	argCount   *int
	formater   Formater
	finalArgs  *[]any
	quoter     Quoter
}

func (w *buildRenderCtx) AddParam(v any) {
	*w.argCount++
	w.sqlBuilder.WriteString(w.formater(*w.argCount))
	*w.finalArgs = append(*w.finalArgs, v)
}

func (w *buildRenderCtx) WriteString(s string) {
	w.sqlBuilder.WriteString(s)
}

func (w *buildRenderCtx) QuoteIdent(s string) {
	w.sqlBuilder.WriteString(w.quoter(s))
}
