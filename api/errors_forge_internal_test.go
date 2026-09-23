package api

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/xraph/relay"
)

// A second replay of the same entry is a conflict with the state the entry is
// already in, not a server failure. It used to fall through to the default
// case and answer 500, which tells an operator to retry: retrying cannot help.
func TestMapErrorAlreadyReplayedIsConflict(t *testing.T) {
	for name, err := range map[string]error{
		"bare":    relay.ErrAlreadyReplayed,
		"wrapped": fmt.Errorf("replay: %w", relay.ErrAlreadyReplayed),
	} {
		var withStatus interface{ StatusCode() int }
		if !errors.As(mapError(err), &withStatus) {
			t.Fatalf("%s: mapError returned no status code", name)
		}
		if got := withStatus.StatusCode(); got != http.StatusConflict {
			t.Fatalf("%s: status = %d, want 409", name, got)
		}
	}
}
