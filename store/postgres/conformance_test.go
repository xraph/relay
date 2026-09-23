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

func TestEngineConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunEngineSuite(t, func(t *testing.T) storetest.EngineBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}

func TestDeliveryConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunDeliverySuite(t, func(t *testing.T) storetest.DeliveryBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}

func TestEventPageConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunEventPageSuite(t, func(t *testing.T) storetest.EventPageBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}

func TestDLQPageConformance(t *testing.T) {
	dsn := startPostgres(t)
	storetest.RunDLQPageSuite(t, func(t *testing.T) storetest.DLQPageBackend {
		t.Helper()
		return openPgStore(t, dsn)
	})
}
