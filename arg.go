package dba

import (
	"fmt"
	"reflect"
	"strings"
)

// ArgWriter 参数渲染出口: Arg 实现者通过它写入占位符与原始文本。
// 实现者不应持有 ArgWriter 超过 WriteTo 调用周期。
type ArgWriter interface {
	// AddParam 写入一个占位符并收集绑定参数 (内部维护序号与方言格式)。
	AddParam(v any)
	// WriteString 写入原始 SQL 文本。
	WriteString(s string)
}

// Arg 参数修饰接口: 实现者自定义该参数在 SQL 中的渲染方式。
//
// build 对 #{} 参数检测 Arg, 命中则委托 WriteTo, 否则按单值绑定。
// 用户可通过实现本接口扩展参数语义, 无需修改 dba (GORM clause.Expression 同款模式)。
type Arg interface {
	WriteTo(w ArgWriter) error
}

// ── 内置 Arg ─────────────────────────────────────────────

// expandArg 展开 slice/array 为独立占位符; orNull=true 时空列表渲染为 NULL。
type expandArg struct {
	v      any
	orNull bool
}

// Expand 展开切片/数组为独立占位符:
//
//	q.Add("WHERE id IN (#{1})", dba.Expand([]int{1, 2})) → IN ($1, $2)
//
// 空切片展开为 0 个参数 (如 `IN ()`), 由数据库报语法错误, 框架不拦截。
// []byte 同样逐字节展开 (显式意图自担)。
func Expand[T any](v []T) Arg { return expandArg{v: v} }

// ExpandOrNull 与 Expand 相同, 但空切片渲染为 NULL 字面量:
//
//	q.Add("WHERE id IN (#{1})", dba.ExpandOrNull([]int{})) → IN (NULL)
func ExpandOrNull[T any](v []T) Arg { return expandArg{v: v, orNull: true} }

func (a expandArg) WriteTo(w ArgWriter) error {
	rv := reflect.ValueOf(a.v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return fmt.Errorf("dba: Expand requires slice or array, got %T", a.v)
	}
	if rv.Len() == 0 {
		if a.orNull {
			w.WriteString("NULL")
		}
		return nil
	}
	for i := 0; i < rv.Len(); i++ {
		if i > 0 {
			w.WriteString(", ")
		}
		w.AddParam(rv.Index(i).Interface())
	}
	return nil
}

// nullArg 渲染 NULL 字面量 (无绑定参数)。
type nullArg struct{}

// Null 渲染为 NULL 字面量。
func Null() Arg { return nullArg{} }

func (nullArg) WriteTo(w ArgWriter) error {
	w.WriteString("NULL")
	return nil
}

// rawArg 参数位置的原始文本注入。
type rawArg struct{ s string }

// Raw 参数位置的原始文本注入 (等价 ! 宏的参数侧形态, 调用者负责安全):
//
//	q.Add("WHERE created_at > #{1}", dba.Raw("NOW()"))
func Raw(s string) Arg { return rawArg{s: s} }

func (r rawArg) WriteTo(w ArgWriter) error {
	w.WriteString(r.s)
	return nil
}

// ── build 侧写入器 ───────────────────────────────────────

// buildArgWriter 连接 Arg 与 build 的 SQL 文本/参数收集。
type buildArgWriter struct {
	sqlBuilder *strings.Builder
	argCount   *int
	formater   Formater
	finalArgs  *[]any
}

func (w *buildArgWriter) AddParam(v any) {
	*w.argCount++
	w.sqlBuilder.WriteString(w.formater(*w.argCount))
	*w.finalArgs = append(*w.finalArgs, v)
}

func (w *buildArgWriter) WriteString(s string) {
	w.sqlBuilder.WriteString(s)
}
