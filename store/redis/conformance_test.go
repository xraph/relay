package redis_test

import (
	"testing"

	"github.com/xraph/relay/store/storetest"
)

// One container for the whole suite. The suite scopes every assertion to rows
// its own subtest created, so a shared keyspace cannot skew a result.
func TestReplayConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunReplaySuite(t, func(t *testing.T) storetest.ReplayBackend {
		t.Helper()
		return openRedisStore(t, connStr)
	})
}

func TestEndpointConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunEndpointSuite(t, func(t *testing.T) storetest.EndpointBackend {
		t.Helper()
		return openRedisStore(t, connStr)
	})
}
