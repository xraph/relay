package relay

import (
	"context"

	"github.com/xraph/relay/acceptance"
)

// SendReliable atomically accepts an event and its complete scoped fanout. The
// host supplies authorized source identity and scope. A receipt proves local
// acceptance, not successful HTTP delivery. Retrying returns the original
// receipt even after catalog changes, endpoint changes or event retention.
func (r *Relay) SendReliable(ctx context.Context, req acceptance.Request) (*acceptance.Receipt, error) {
	s, ok := r.store.(acceptance.Store)
	if !ok {
		return nil, acceptance.ErrUnsupported
	}
	req, err := req.Normalize()
	if err != nil {
		return nil, err
	}
	receipt, err := s.AcceptEvent(ctx, req, r.config.MaxRetries)
	if err != nil {
		return nil, err
	}
	r.engine.Wake()
	return receipt, nil
}
