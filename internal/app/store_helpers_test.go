package app

import (
	"database/sql"
	"testing"

	"github.com/Icewater1337/fridolin/internal/store"
)

func openTestStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath, "error")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		s.Container.Close()
	})
	return s
}

// testStoreSQL opens a separate connection for assertions and injected database
// failures. Store's owned connection stays private to its package.
func testStoreSQL(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
