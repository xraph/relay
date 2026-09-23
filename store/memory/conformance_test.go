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
