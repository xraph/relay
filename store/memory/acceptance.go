package memory

import (
	"context"
	"sort"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/acceptanceobs"
	"github.com/xraph/relay/internal/acceptanceutil"
)

var _ acceptance.Store = (*Store)(nil)

func (s *Store) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	req, err := req.Normalize()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if checkErr := ctx.Err(); checkErr != nil {
		return nil, checkErr
	}
	if s.closed {
		return nil, relay.ErrStoreClosed
	}
	if receipt := s.receipts[req.Identity()]; receipt != nil {
		if checkErr := receipt.Verify(req); checkErr != nil {
			return nil, checkErr
		}
		return receipt.Clone(), nil
	}
	if maxAttempts < 1 {
		return nil, acceptance.ErrInvalid
	}
	if checkErr := acceptanceutil.Validate(s.eventTypes[req.Type], req); checkErr != nil {
		return nil, checkErr
	}
	endpointIDs := []id.ID{}
	candidates := 0
	for _, ep := range s.endpoints {
		if !ep.Enabled || ep.ScopeAppID != req.AppID || ep.ScopeOrgID != req.OrgID || ep.TenantID != req.TenantID {
			continue
		}
		candidates++
		if candidates > acceptance.MaxRecipients {
			return nil, acceptance.ErrTooManyRecipients
		}
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, req.Type) {
				endpointIDs = append(endpointIDs, ep.ID)
				break
			}
		}
		if len(endpointIDs) > acceptance.MaxRecipients {
			return nil, acceptance.ErrTooManyRecipients
		}
	}
	sort.Slice(endpointIDs, func(i, j int) bool { return endpointIDs[i].String() < endpointIDs[j].String() })
	evt, ds, receipt, err := acceptanceutil.Build(req, endpointIDs, maxAttempts)
	if err != nil {
		return nil, err
	}
	if checkErr := ctx.Err(); checkErr != nil {
		return nil, checkErr
	}
	// All fallible work finishes before any map is changed.
	s.events[evt.ID.String()] = evt
	for _, d := range ds {
		s.deliveries[d.ID.String()] = d
	}
	s.receipts[req.Identity()] = receipt
	acceptanceobs.MarkNewCommit(ctx)
	return receipt.Clone(), nil
}
