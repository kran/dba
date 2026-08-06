package dba_test

import (
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/kran/dba"
)

// struct 字段的 Node: 整体单列, 值内联 (参数即子树对 struct 源闭环)。
type nodeEvent struct {
	Name    string   `db:"name"`
	Created dba.Node `db:"created"`
}

type nodeEventPtr struct {
	Name    string    `db:"name"`
	Created *dba.Node `db:"created"`
}

func TestNodeFieldInStruct(t *testing.T) {
	q, _ := newQ(t)
	// Node 字段 (值): 内联 NOW(), name 是普通参数
	_, args, err := q.Insert("events", nodeEvent{Name: "e", Created: dba.Expr("NOW()")}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "e" {
		t.Fatalf("node field args: %v", args)
	}
	// *Node 字段 (指针): 同样单列, nil 指针 = NULL
	_, args, err = q.Insert("events", nodeEventPtr{Name: "e", Created: ptrNode(dba.Expr("NOW()"))}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "e" {
		t.Fatalf("node ptr field args: %v", args)
	}
	_, args, err = q.Insert("events", nodeEventPtr{Name: "e"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "e" || args[1] != nil {
		t.Fatalf("nil node ptr field args: %v", args)
	}
}

func ptrNode(n dba.Node) *dba.Node { return &n }

// omitempty × 指针: nil 跳过, &0 显式保留 (逃生舱 — encoding/json 约定)。
type ptrZeroModel struct {
	Name  string `db:"name"`
	Views *int   `db:"views,omitempty"`
}

func TestOmitemptyPtrEscapHatch(t *testing.T) {
	q, _ := newQ(t)
	// nil 指针 → omitempty 跳过
	_, args, err := q.Insert("t", ptrZeroModel{Name: "n"}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "n" {
		t.Fatalf("nil ptr args: %v", args)
	}
	// &0 → 非 nil 显式赋值 → 保留并绑定 0
	zero := 0
	_, args, err = q.Insert("t", ptrZeroModel{Name: "n", Views: &zero}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 2 || args[0] != "n" || args[1] != 0 {
		t.Fatalf("&0 args: %v", args)
	}
}

// normalizeBindValue 全形态: 指针逃生舱 + Valuer 守卫 + 别名 Convert。
type myTime time.Time // 无 Valuer 的 time.Time 别名

type myTimeValuer time.Time

func (t *myTimeValuer) Value() (driver.Value, error) { // 指针接收者 Valuer
	return time.Time(*t), nil
}

type escapeModel struct {
	A *int           `db:"a,omitempty"` // nil → 跳过
	B *int           `db:"b,omitempty"` // &0 → 保留, 绑 int(0)
	C myTime         `db:"c"`           // 别名 → Convert 为 time.Time
	D *myTime        `db:"d"`           // 非 nil → time.Time
	E *dba.Node      `db:"e"`           // → Node (值, 走 Bind 内联)
	F sql.NullString `db:"f,omitempty"` // 零值 → 跳过
	G *myTimeValuer  `db:"g"`           // 指针接收者 Valuer → 指针保留
}

func TestNormalizeBindValue(t *testing.T) {
	q, _ := newQ(t)
	zero := 0
	now := time.Now()
	mt := myTime(now)
	mtv := myTimeValuer(now)
	sql, args, err := q.Insert("t", escapeModel{
		B: &zero, C: mt, D: &mt, E: ptrNode(dba.Expr("NOW()")), G: &mtv,
	}).ToSQL()
	if err != nil {
		t.Fatal(err)
	}
	// 4 个绑定参数: b/c/d/g — e (Node) 内联进 SQL 文本不占参数位
	if len(args) != 4 {
		t.Fatalf("args len: %d %v", len(args), args)
	}
	if args[0] != 0 {
		t.Fatalf("b should be int 0: %#v", args[0])
	}
	if tv, ok := args[1].(time.Time); !ok || !tv.Equal(now) {
		t.Fatalf("c should be time.Time: %#v", args[1])
	}
	if tv, ok := args[2].(time.Time); !ok || !tv.Equal(now) {
		t.Fatalf("d should be time.Time: %#v", args[2])
	}
	if _, ok := args[3].(*myTimeValuer); !ok {
		t.Fatalf("g should keep pointer (ptr-receiver Valuer): %#v", args[3])
	}
	// e: Node 内联为 SQL 文本 (NOW())
	if !strings.Contains(sql, "NOW()") {
		t.Fatalf("e should inline NOW(): %s", sql)
	}
}

// 空 Node 护栏: Render 空片段报错, 不产出空洞 SQL。
func TestEmptyNodeGuard(t *testing.T) {
	q, _ := newQ(t)
	if _, _, err := q.Add("VALUES (#{1})", dba.Expr("")).ToSQL(); err == nil {
		t.Fatal("empty Node should error")
	}
}
