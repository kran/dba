package dba

import (
	"fmt"
	"github.com/jmoiron/sqlx/reflectx"
	"maps"
	"reflect"
	"strconv"
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
	// Resolve 把宏内容作为参数 key 解析 (位置/命名); 管道按需调用。
	Resolve(key string) (any, error)
}

// Pipe 管道: 宏内容的解读者。
//
// 管道收到宏内容的字面量 (如 #{1|pipe} 的 "1"、@{users} 的 "users"),
// 由管道自行决定: 字面量直接用 (ident), 或经 ctx.Resolve 取参数 (bind/raw/expand)。
// 用户管道可自由选择 — 这是管道的灵活性所在。
// 内置: bind/expand/raw/ident; 用户通过 RegisterPipe 注册。
type Pipe func(ctx RenderCtx, content string) error

// ── 内置管道 ─────────────────────────────────────────────

func pipeBind(ctx RenderCtx, content string) error {
	v, err := ctx.Resolve(content)
	if err != nil {
		return err
	}
	ctx.AddParam(v)
	return nil
}

// pipeExpand 展开 slice/array 为独立占位符:
//
//	q.Add("WHERE id IN (#{1|expand})", []int{1, 2}) → IN ($1, $2)
//
// 空切片展开为 0 个参数 (如 `IN ()`), 由数据库报语法错误, 框架不拦截。
// []byte 同样逐字节展开 (显式意图自担)。
func pipeExpand(ctx RenderCtx, content string) error {
	v, err := ctx.Resolve(content)
	if err != nil {
		return err
	}
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
func pipeRaw(ctx RenderCtx, content string) error {
	v, err := ctx.Resolve(content)
	if err != nil {
		return err
	}
	if v == nil {
		return fmt.Errorf("dba: raw pipe requires non-nil value")
	}
	ctx.WriteString(fmt.Sprintf("%v", v))
	return nil
}

// pipeQuote 参数版: 内容为参数 key, 对参数值 quote (动态标识符场景):
//
//	q.Add("SELECT #{1|quote} FROM t", "name")
func pipeQuote(ctx RenderCtx, content string) error {
	v, err := ctx.Resolve(content)
	if err != nil {
		return err
	}
	if v == nil {
		return fmt.Errorf("dba: quote pipe requires non-nil value")
	}
	ctx.QuoteIdent(fmt.Sprintf("%v", v))
	return nil
}

// pipeLiteralQuote 字面量版: 内容即标识符名 (@{users} 场景, 不需要参数)。
func pipeLiteralQuote(ctx RenderCtx, content string) error {
	if content == "" {
		return fmt.Errorf("dba: literalquote pipe requires non-empty identifier")
	}
	ctx.QuoteIdent(content)
	return nil
}

// defaultPipes 内置管道集 (New 时初始化到实例)。
var defaultPipes = map[string]Pipe{
	"bind":         pipeBind,
	"expand":       pipeExpand,
	"raw":          pipeRaw,
	"quote":        pipeQuote,        // 参数版 (动态标识符: #{1|quote})
	"literalquote": pipeLiteralQuote, // 字面量版 (@{users}: 内容即标识符)
}

// defaultMacros 宏表: 前缀 → 默认管道 (内容可带 |pipe 覆盖)。
// '#' 只是默认 bind 的普通宏; '$' 为保留前缀 (结构层, 不进表)。
var defaultMacros = map[byte]string{
	'#': "bind",         // #{1} → bind
	'@': "literalquote", // @{users} → 字面量 quote (内容即标识符名)
	'!': "raw",          // !{1} → raw (参数)
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
	args       []any // 当前渲染参数列表 (render 入口更新, 递归时变化)
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

func (w *buildRenderCtx) Resolve(key string) (any, error) {
	return resolveArg(w.args, key)
}

func resolveArg(args []any, content string) (any, error) {
	if idx, err := strconv.Atoi(content); err == nil {
		if idx < 1 || idx > len(args) {
			return nil, fmt.Errorf("dba: index %d out of bounds", idx)
		}
		return args[idx-1], nil
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("dba: no args")
	}
	return extractNamedArg(args[len(args)-1], content)
}

var mapper = reflectx.NewMapperFunc("db", strings.ToLower)

func extractNamedArg(src any, name string) (any, error) {
	rv := reflect.ValueOf(src)
	for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, fmt.Errorf("dba: named args source is nil pointer")
		}
		rv = rv.Elem()
	}

	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("dba: map key must be string")
		}
		val := rv.MapIndex(reflect.ValueOf(name))
		if !val.IsValid() {
			return nil, fmt.Errorf("dba: named arg '%s' not found in map", name)
		}
		return val.Interface(), nil
	}

	if rv.Kind() == reflect.Struct {
		fm := mapper.TypeMap(rv.Type())
		fi := fm.GetByPath(name)
		if fi == nil {
			return nil, fmt.Errorf("dba: field '%s' not found in struct", name)
		}
		return reflectx.FieldByIndexesReadOnly(rv, fi.Index).Interface(), nil
	}

	return nil, fmt.Errorf("dba: named args source must be struct or map")
}
