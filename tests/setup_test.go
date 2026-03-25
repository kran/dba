package sqlo_test

import (
	"codeberg.org/kran/sqlo"
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

func newQ(t *testing.T) (*sqlo.Sqlo, *sqlx.DB) {
	t.Helper()
	db := newDB(t)
	return sqlo.New(db).Format(sqlo.DollarFormat), db
}
