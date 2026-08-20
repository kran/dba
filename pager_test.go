package dba

// Pager 方言渲染的内部测试:
//   - 两个内置渲染器的输出形态
//   - NewFromSqlx 按 driverName 自动探测 (假驱动注册, sqlx.Open 惰性不连接)
//   - Pager() 覆盖 copy-on-write, 不影响原实例

import (
	"database/sql"
	"database/sql/driver"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, nil }

var registerOnce sync.Once

func registerFakeDrivers() {
	registerOnce.Do(func() {
		names := []string{"postgres", "pgx", "mysql", "sqlite", "sqlite3", "sqlserver", "oracle", "db2", "unknownfake"}
		for _, n := range names {
			sql.Register(n, fakeDriver{})
		}
	})
}

func TestPager_Builtins(t *testing.T) {
	if got := LimitOffsetPager("#{1}", "#{2}"); got != "LIMIT #{1} OFFSET #{2}" {
		t.Errorf("LimitOffsetPager: %q", got)
	}
	if got := OffsetFetchPager("#{1}", "#{2}"); got != "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY" {
		t.Errorf("OffsetFetchPager: %q", got)
	}
}

func TestPager_AutoDetect(t *testing.T) {
	registerFakeDrivers()

	cases := []struct {
		driver string
		want   string
	}{
		{"unknownfake", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"}, // 默认: SQL:2008 标准
		{"postgres", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"},    // pg 两套语法都支持, 走标准
		{"pgx", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"},
		{"mysql", "LIMIT #{1} OFFSET #{2}"},   // mysql 系例外
		{"sqlite", "LIMIT #{1} OFFSET #{2}"},  // sqlite 系例外 (modernc)
		{"sqlite3", "LIMIT #{1} OFFSET #{2}"}, // sqlite 系例外 (mattn)
		{"sqlserver", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"},
		{"oracle", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"},
		{"db2", "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY"},
	}

	for _, c := range cases {
		db, err := sqlx.Open(c.driver, "dsn") // 惰性: 不真正连接
		if err != nil {
			t.Fatalf("%s: %v", c.driver, err)
		}
		q := NewFromSqlx(db)
		if got := q.pager("#{1}", "#{2}"); got != c.want {
			t.Errorf("%s: got %q, want %q", c.driver, got, c.want)
		}
	}
}

func TestPager_OverrideCopyOnWrite(t *testing.T) {
	registerFakeDrivers()
	db, err := sqlx.Open("unknownfake", "dsn")
	if err != nil {
		t.Fatal(err)
	}
	q := NewFromSqlx(db)

	custom := func(limit, offset string) string { return "CUSTOM " + limit + " / " + offset }
	q2 := q.Pager(custom)

	// 覆盖生效
	if got := q2.pager("#{1}", "#{2}"); got != "CUSTOM #{1} / #{2}" {
		t.Errorf("override: %q", got)
	}
	// 原实例不受影响 (unknownfake → 标准默认)
	if got := q.pager("#{1}", "#{2}"); got != "OFFSET #{2} ROWS FETCH NEXT #{1} ROWS ONLY" {
		t.Errorf("original polluted: %q", got)
	}
}
