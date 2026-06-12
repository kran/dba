package dba_test

import (
	"bytes"
	"codeberg.org/kran/dba"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMiddleware_Logging(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test (id INTEGER PRIMARY KEY, val TEXT)")
	db.Exec("INSERT INTO mw_test VALUES (1, 'hello')")

	var captured struct {
		query string
		args  []any
		err   error
	}

	logged := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			result, err := next(ctx, query, args)
			captured.query = query
			captured.args = args
			captured.err = err
			return result, err
		}
	})

	var val string
	logged.Add("SELECT val FROM mw_test WHERE id = #{1}", 1).Get(&val)

	if captured.query == "" {
		t.Error("middleware not called")
	}
	if val != "hello" {
		t.Errorf("expected hello, got %q", val)
	}
	if captured.err != nil {
		t.Errorf("unexpected error: %v", captured.err)
	}
}

func TestMiddleware_ShortCircuit(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test2 (id INTEGER PRIMARY KEY)")

	blocked := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			return nil, errors.New("blocked")
		}
	})

	_, err := blocked.Add("INSERT INTO mw_test2 VALUES (1)").Exec()
	if err == nil || err.Error() != "blocked" {
		t.Errorf("expected blocked error, got %v", err)
	}
}

func TestMiddleware_Chain_Order(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test3 (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO mw_test3 VALUES (1)")

	var order []string
	var mu sync.Mutex
	record := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	chained := q.
		Use(func(next dba.ExecFunc) dba.ExecFunc {
			return func(ctx context.Context, query string, args []any) (any, error) {
				record("A-before")
				result, err := next(ctx, query, args)
				record("A-after")
				return result, err
			}
		}).
		Use(func(next dba.ExecFunc) dba.ExecFunc {
			return func(ctx context.Context, query string, args []any) (any, error) {
				record("B-before")
				result, err := next(ctx, query, args)
				record("B-after")
				return result, err
			}
		})

	count, _, err := dba.Scalar[int](chained.Add("SELECT COUNT(1) FROM mw_test3"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	// 洋葱模型: A 包裹 B 包裹实际执行
	want := []string{"A-before", "B-before", "B-after", "A-after"}
	if len(order) != len(want) {
		t.Fatalf("order: %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d]: got %q, want %q", i, order[i], want[i])
		}
	}
}

func TestMiddleware_Immutable(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test4 (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO mw_test4 VALUES (1)")

	called := false
	withMW := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			called = true
			return next(ctx, query, args)
		}
	})

	// 原始 q 不受影响
	q.Add("SELECT COUNT(1) FROM mw_test4").Get(new(int))
	if called {
		t.Error("middleware should not affect original instance")
	}

	// withMW 才触发
	withMW.Add("SELECT COUNT(1) FROM mw_test4").Get(new(int))
	if !called {
		t.Error("middleware not called on Use'd instance")
	}
}

func TestMiddleware_WithExec(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test5 (id INTEGER PRIMARY KEY, val INTEGER)")

	var capturedQuery string
	logged := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			capturedQuery = query
			return next(ctx, query, args)
		}
	})

	logged.Insert("mw_test5", map[string]any{"id": 1, "val": 10}).Exec()
	if capturedQuery == "" {
		t.Error("middleware not called on Exec")
	}
}

func TestMiddleware_WithRows(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test6 (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO mw_test6 VALUES (1)")
	db.Exec("INSERT INTO mw_test6 VALUES (2)")

	callCount := 0
	logged := q.Use(func(next dba.ExecFunc) dba.ExecFunc {
		return func(ctx context.Context, query string, args []any) (any, error) {
			callCount++
			return next(ctx, query, args)
		}
	})

	rows, err := logged.Add("SELECT id FROM mw_test6").Rows()
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		rows.Scan(&id)
		ids = append(ids, id)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 rows, got %d", len(ids))
	}
	if callCount != 1 {
		t.Errorf("middleware should fire once, fired %d times", callCount)
	}
}

func TestLogMiddleware_Debug(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE log_test (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO log_test VALUES (1)")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logged := q.Use(dba.LogHook(logger, 0, true))
	count, _, err := dba.Scalar[int](logged.Add("SELECT COUNT(1) FROM log_test"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1, got %d", count)
	}

	output := buf.String()
	if !strings.Contains(output, "SQL") {
		t.Errorf("log should contain SQL, got: %s", output)
	}
	if !strings.Contains(output, "SELECT COUNT(1) FROM log_test") {
		t.Errorf("log should contain cleaned query, got: %s", output)
	}
	if !strings.Contains(output, "duration") {
		t.Errorf("log should contain duration, got: %s", output)
	}
}

func TestLogMiddleware_SlowQuery(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE log_test2 (id INTEGER PRIMARY KEY)")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// 1ns 阈值，任何查询都算慢
	logged := q.Use(dba.LogHook(logger, 1*time.Nanosecond, true))
	logged.Add("SELECT 1").Get(new(int))

	output := buf.String()
	if !strings.Contains(output, "SLOW SQL") {
		t.Errorf("expected SLOW SQL warning, got: %s", output)
	}
	if !strings.Contains(output, "WARN") {
		t.Errorf("expected WARN level, got: %s", output)
	}
}

func TestLogMiddleware_Error(t *testing.T) {
	q, _ := newQ(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logged := q.Use(dba.LogHook(logger, 0, true))
	// 查询不存在的表，触发错误
	logged.Add("SELECT 1 FROM nonexistent_table").Get(new(int))

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Errorf("expected ERROR level, got: %s", output)
	}
}
