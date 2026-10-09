package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"

	"github.com/xraph/grove"

	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/acceptanceutil"
)

var _ acceptance.Store = (*Store)(nil)

type acceptanceModel struct {
	grove.BaseModel `grove:"table:relay_acceptances"`
	Identity        string          `grove:"identity,pk"`
	Receipt         json.RawMessage `grove:"receipt,type:jsonb"`
}

func (s *Store) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	req, err := req.Normalize()
	if err != nil {
		return nil, err
	}
	tx, err := s.pg.BeginTxQuery(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }() //nolint:errcheck // rollback also runs after a successful commit
	// Hash collisions only serialize unrelated requests. The exact identity is
	// still the primary key and the receipt is checked before disclosure.
	sum := sha256.Sum256([]byte(req.Identity()))
	lockID := int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // all 64 bits intentionally form a signed advisory lock ID
	if _, err = tx.NewRaw("SELECT pg_advisory_xact_lock($1)", lockID).Exec(ctx); err != nil {
		return nil, err
	}
	m := new(acceptanceModel)
	err = tx.NewSelect(m).Where("identity = $1", req.Identity()).Scan(ctx)
	if err == nil {
		var receipt acceptance.Receipt
		if checkErr := json.Unmarshal(m.Receipt, &receipt); checkErr != nil {
			return nil, checkErr
		}
		if checkErr := receipt.Verify(req); checkErr != nil {
			return nil, checkErr
		}
		return &receipt, nil
	}
	if !isNoRows(err) {
		return nil, err
	}
	if maxAttempts < 1 {
		return nil, acceptance.ErrInvalid
	}
	et := new(eventTypeModel)
	if err = tx.NewSelect(et).Where("name = $1", req.Type).Scan(ctx); err != nil {
		if isNoRows(err) {
			return nil, acceptanceutil.Validate(nil, req)
		}
		return nil, err
	}
	eventType, err := fromEventTypeModel(et)
	if err != nil {
		return nil, err
	}
	if checkErr := acceptanceutil.Validate(eventType, req); checkErr != nil {
		return nil, checkErr
	}
	// The statement snapshot pins membership at selection time. Endpoint edits
	// after it may change delivery-time configuration, but cannot add recipients.
	var endpoints []endpointModel
	if checkErr := tx.NewSelect(&endpoints).Where("scope_app_id = $1", req.AppID).Where("scope_org_id = $2", req.OrgID).Where("tenant_id = $3", req.TenantID).Where("enabled = TRUE").OrderExpr("id ASC").Limit(acceptance.MaxRecipients + 1).Scan(ctx); checkErr != nil {
		return nil, checkErr
	}
	// Bound the candidate set as well as the resulting batch.
	if len(endpoints) > acceptance.MaxRecipients {
		return nil, acceptance.ErrTooManyRecipients
	}
	endpointIDs := []id.ID{}
	for _, ep := range endpoints {
		for _, pattern := range ep.EventTypes {
			if catalog.Match(pattern, req.Type) {
				epID, e := id.ParseEndpointID(ep.ID)
				if e != nil {
					return nil, e
				}
				endpointIDs = append(endpointIDs, epID)
				break
			}
		}
	}
	evt, ds, receipt, err := acceptanceutil.Build(req, endpointIDs, maxAttempts)
	if err != nil {
		return nil, err
	}
	if _, err = tx.NewInsert(toEventModel(evt)).Exec(ctx); err != nil {
		return nil, err
	}
	for _, d := range ds {
		if _, err = tx.NewInsert(toDeliveryModel(d)).Exec(ctx); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.NewInsert(&acceptanceModel{Identity: req.Identity(), Receipt: raw}).Exec(ctx); err != nil {
		return nil, err
	}
	if checkErr := tx.Commit(); checkErr != nil {
		return nil, checkErr
	}
	s.notifyWake(ctx)
	return receipt, nil
}
