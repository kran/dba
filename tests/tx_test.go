package stupidql_test

import (
	"errors"
	"testing"

	"stupidql"
)

func setupItemsTable(t *testing.T, q *stupidql.StupidQL) {
	t.Helper()
	_, err := q.Add("CREATE TABLE items (id INTEGER PRIMARY KEY, val TEXT)").Exec()
	if err != nil {
		t.Fatal(err)
	}
}

func countItems(t *testing.T, q *stupidql.StupidQL) int {
	t.Helper()
	var n int
	if err := q.Add("SELECT COUNT(*) FROM items").Get(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTransaction_Commit(t *testing.T) {
	q, _ := newQ(t)
	setupItemsTable(t, q)

	err := q.Transaction(func(tx *stupidql.StupidQL) error {
		_, err := tx.Insert("items", map[string]any{"id": 1, "val": "hello"}).Exec()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if n := countItems(t, q); n != 1 {
		t.Errorf("expected 1 row after commit, got %d", n)
	}
}

func TestTransaction_RollbackOnError(t *testing.T) {
	q, _ := newQ(t)
	setupItemsTable(t, q)

	err := q.Transaction(func(tx *stupidql.StupidQL) error {
		if _, err := tx.Insert("items", map[string]any{"id": 1, "val": "hello"}).Exec(); err != nil {
			return err
		}
		return errors.New("intentional error")
	})
	if err == nil {
		t.Error("expected error")
	}

	if n := countItems(t, q); n != 0 {
		t.Errorf("expected 0 rows after rollback, got %d", n)
	}
}

func TestTransaction_RollbackOnPanic(t *testing.T) {
	q, _ := newQ(t)
	setupItemsTable(t, q)

	func() {
		defer func() { recover() }()
		_ = q.Transaction(func(tx *stupidql.StupidQL) error {
			tx.Insert("items", map[string]any{"id": 1, "val": "hello"}).Exec() //nolint
			panic("test panic")
		})
	}()

	if n := countItems(t, q); n != 0 {
		t.Errorf("expected 0 rows after panic rollback, got %d", n)
	}
}

func TestTransaction_NestedBeginFails(t *testing.T) {
	q, _ := newQ(t)
	setupItemsTable(t, q)

	err := q.Transaction(func(tx *stupidql.StupidQL) error {
		_, err := tx.Begin()
		if err == nil {
			t.Error("expected error on nested Begin")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBeginCommitRollback_Manual(t *testing.T) {
	q, _ := newQ(t)
	setupItemsTable(t, q)

	tx, err := q.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Insert("items", map[string]any{"id": 1, "val": "a"}).Exec(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if n := countItems(t, q); n != 1 {
		t.Errorf("expected 1 row, got %d", n)
	}

	// Rollback after Commit should fail silently (sql.ErrTxDone)
	if err := tx.Rollback(); err == nil {
		t.Error("expected error on rollback after commit")
	}
}
