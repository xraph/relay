package relay_test

import (
	"net/http"
	"testing"

	"github.com/xraph/relay"
	"github.com/xraph/relay/store/memory"
)

func TestWithHTTPClientReachesTheConfig(t *testing.T) {
	client := &http.Client{}

	r, err := relay.New(relay.WithStore(memory.New()), relay.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}

	if r.Config().HTTPClient != client {
		t.Fatal("WithHTTPClient did not reach the config")
	}

	if _, err := relay.New(relay.WithStore(memory.New()), relay.WithHTTPClient(nil)); err == nil {
		t.Fatal("a nil client was accepted")
	}
}
