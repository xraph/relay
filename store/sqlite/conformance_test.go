package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/xraph/grove"
	"github.com/xraph/grove/drivers/sqlitedriver"
	_ "github.com/xraph/grove/drivers/sqlitedriver/sqlitemigrate" // registers the sqlite migrate executor

	sqlitestore "github.com/xraph/relay/store/sqlite"
	"github.com/xraph/relay/store/storetest"
)

// A fresh file per subtest. Not ":memory:": with a connection pool every
// connection gets its own empty database, so a table migrated on one would
// not exist on the next.
func openSqliteStore(t *testing.T) *sqlitestore.Store {
	t.Helper()
	ctx := context.Background()

	drv := sqlitedriver.New()
	// busy_timeout lets a writer wait for the lock instead of failing at
	// once with SQLITE_BUSY. It is per connection, so it has to be in the
	// DSN to reach every connection in the pool; a PRAGMA run after opening
	// would configure only one of them. Without it, concurrent claims fail
	// with "database is locked" (never with two winners: the claim is still
	// atomic, the loser just errors instead of waiting its turn).
	dsn := "file:" + filepath.Join(t.TempDir(), "relay.db") + "?_pragma=busy_timeout(5000)"
	if err := drv.Open(ctx, dsn); err != nil {
		t.Fatalf("open sqlitedriver: %v", err)
	}
	db, err := grove.Open(drv)
	if err != nil {
		t.Fatalf("grove open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s := sqlitestore.New(db)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestReplayConformance(t *testing.T) {
	storetest.RunReplaySuite(t, func(t *testing.T) storetest.ReplayBackend {
		t.Helper()
		return openSqliteStore(t)
	})
}

func TestEndpointConformance(t *testing.T) {
	storetest.RunEndpointSuite(t, func(t *testing.T) storetest.EndpointBackend {
		t.Helper()
		return openSqliteStore(t)
	})
}
