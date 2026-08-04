package dba_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kran/dba"
)

func TestSetLogger_Logging(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test (id INTEGER PRIMARY KEY, val TEXT)")
	db.Exec("INSERT INTO mw_test VALUES (1, 'hello')")

	var captured struct {
		query string
		args  []any
		err   error
	}
	logged := q.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		captured.query = query
		captured.args = args
		captured.err = err
	})

	var val string
	logged.Add("SELECT val FROM mw_test WHERE id = #{1}", 1).Get(&val)

	if captured.query == "" {
		t.Error("logger not called")
	}
	if val != "hello" {
		t.Errorf("expected hello, got %q", val)
	}
	if captured.err != nil {
		t.Errorf("unexpected error: %v", captured.err)
	}
}

func TestSetLogger_ErrorStillFires(t *testing.T) {
	q, _ := newQ(t)

	var lastErr error
	logged := q.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		lastErr = err
	})

	logged.Add("SELECT 1 FROM nonexistent_table").Get(new(int))
	if lastErr == nil {
		t.Error("logger should fire with error")
	}
}

func TestSetLogger_Immutable(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test4 (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO mw_test4 VALUES (1)")

	called := false
	withMW := q.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		called = true
	})

	// 原始 q 不受影响
	q.Add("SELECT COUNT(1) FROM mw_test4").Get(new(int))
	if called {
		t.Error("logger should not affect original instance")
	}

	// withMW 才触发
	withMW.Add("SELECT COUNT(1) FROM mw_test4").Get(new(int))
	if !called {
		t.Error("logger not called on SetLogger'd instance")
	}
}

func TestSetLogger_WithExec(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test5 (id INTEGER PRIMARY KEY, val INTEGER)")

	var capturedQuery string
	logged := q.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		capturedQuery = query
	})

	logged.Insert("mw_test5", map[string]any{"id": 1, "val": 10}).Exec()
	if capturedQuery == "" {
		t.Error("logger not called on Exec")
	}
}

func TestSetLogger_WithRows(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE mw_test6 (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO mw_test6 VALUES (1)")
	db.Exec("INSERT INTO mw_test6 VALUES (2)")

	callCount := 0
	logged := q.SetLogger(func(ctx context.Context, begin time.Time, query string, args []any, err error) {
		callCount++
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
		t.Errorf("logger should fire once, fired %d times", callCount)
	}
}

func TestLogHook_Debug(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE log_test (id INTEGER PRIMARY KEY)")
	db.Exec("INSERT INTO log_test VALUES (1)")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logged := q.SetLogger(dba.NewLogger(logger, 0, true))
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

func TestLogHook_SlowQuery(t *testing.T) {
	q, db := newQ(t)
	db.Exec("CREATE TABLE log_test2 (id INTEGER PRIMARY KEY)")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// 1ns 阈值，任何查询都算慢
	logged := q.SetLogger(dba.NewLogger(logger, 1*time.Nanosecond, true))
	logged.Add("SELECT 1").Get(new(int))

	output := buf.String()
	if !strings.Contains(output, "SLOW SQL") {
		t.Errorf("expected SLOW SQL warning, got: %s", output)
	}
	if !strings.Contains(output, "WARN") {
		t.Errorf("expected WARN level, got: %s", output)
	}
}

func TestLogHook_Error(t *testing.T) {
	q, _ := newQ(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logged := q.SetLogger(dba.NewLogger(logger, 0, true))
	// 查询不存在的表，触发错误
	logged.Add("SELECT 1 FROM nonexistent_table").Get(new(int))

	output := buf.String()
	if !strings.Contains(output, "ERROR") {
		t.Errorf("expected ERROR level, got: %s", output)
	}
}
