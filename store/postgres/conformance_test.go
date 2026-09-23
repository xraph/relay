package postgres_test

import (
	"testing"

	"github.com/xraph/relay/store/storetest"
)

// One container for the whole suite. The suite scopes every assertion to rows
// its own subtest created, so a shared database cannot skew a result.
func TestReplayConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunReplaySuite(t, func(t *testing.T) storetest.ReplayBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}

func TestEndpointConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunEndpointSuite(t, func(t *testing.T) storetest.EndpointBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}
