package mongo_test

import (
	"testing"

	"github.com/xraph/relay/store/storetest"
)

// One container for the whole suite. The suite scopes every assertion to rows
// its own subtest created, so a shared database cannot skew a result.
func TestReplayConformance(t *testing.T) {
	uri := startMongo(t)
	storetest.RunReplaySuite(t, func(t *testing.T) storetest.ReplayBackend {
		t.Helper()
		return openStore(t, uri)
	})
}
