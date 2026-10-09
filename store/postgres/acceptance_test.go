package postgres_test

import (
	"testing"

	"github.com/xraph/relay/store/storetest"
)

func TestReliableAcceptance(t *testing.T) {
	storetest.RunAcceptanceSuite(t, openPgStore(t, startPostgres(t)))
}
