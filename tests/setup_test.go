package stupidql_test

import (
	"testing"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
	"stupidql"
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

func newQ(t *testing.T) (*stupidql.StupidQL, *sqlx.DB) {
	t.Helper()
	db := newDB(t)
	return stupidql.NewStupidQL(db), db
}
