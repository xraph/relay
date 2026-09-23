package memory_test

import (
	"testing"

	"github.com/xraph/relay/store/memory"
	"github.com/xraph/relay/store/storetest"
)

func TestReplayConformance(t *testing.T) {
	storetest.RunReplaySuite(t, func(t *testing.T) storetest.ReplayBackend {
		t.Helper()
		return memory.New()
	})
}

func TestEndpointConformance(t *testing.T) {
	storetest.RunEndpointSuite(t, func(t *testing.T) storetest.EndpointBackend {
		t.Helper()
		return memory.New()
	})
}
