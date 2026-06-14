package dba_test

import (
	"github.com/kran/dba"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func newDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newQ(t *testing.T) (*dba.SQL, *sqlx.DB) {
	t.Helper()
	db := newDB(t)
	return dba.New(db).Format(dba.DollarFormat), db
}
