package redis_test

import (
	"context"
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
		s := openRedisStore(t, connStr)
		// An every-tenant list refuses until Migrate has built the global
		// index, and a real deployment runs Migrate at startup.
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return s
	})
}

func TestEngineConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunEngineSuite(t, func(t *testing.T) storetest.EngineBackend {
		t.Helper()
		return openRedisStore(t, connStr)
	})
}

func TestDeliveryConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunDeliverySuite(t, func(t *testing.T) storetest.DeliveryBackend {
		t.Helper()
		s := openRedisStore(t, connStr)
		// The every-delivery list needs the global index, which Migrate
		// builds, exactly as a deployment does.
		if err := s.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return s
	})
}

func TestEventPageConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunEventPageSuite(t, func(t *testing.T) storetest.EventPageBackend {
		t.Helper()
		return openRedisStore(t, connStr)
	})
}

func TestDLQPageConformance(t *testing.T) {
	connStr := startRedis(t)
	storetest.RunDLQPageSuite(t, func(t *testing.T) storetest.DLQPageBackend {
		t.Helper()
		return openRedisStore(t, connStr)
	})
}
