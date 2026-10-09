package memory_test

import (
	"testing"

	"github.com/xraph/relay/store/memory"
	"github.com/xraph/relay/store/storetest"
)

func TestReliableAcceptance(t *testing.T) { storetest.RunAcceptanceSuite(t, memory.New()) }
