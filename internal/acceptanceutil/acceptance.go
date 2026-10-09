// Package acceptanceutil shares transaction-independent acceptance preparation.
package acceptanceutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

func Validate(et *catalog.EventType, req acceptance.Request) error {
	if et == nil {
		return relay.ErrEventTypeNotFound
	}
	if et.IsDeprecated {
		return relay.ErrEventTypeDeprecated
	}
	if len(et.Definition.Schema) == 0 {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(req.Data))
	d.UseNumber()
	var value any
	if err := d.Decode(&value); err != nil {
		return err
	}
	if err := catalog.NewValidator().Validate(et.Definition.Schema, value); err != nil {
		return fmt.Errorf("%w: %w", relay.ErrPayloadValidationFailed, err)
	}
	return nil
}

func Build(req acceptance.Request, endpointIDs []id.ID, maxAttempts int) (*event.Event, []*delivery.Delivery, *acceptance.Receipt, error) {
	if len(endpointIDs) > acceptance.MaxRecipients {
		return nil, nil, nil, acceptance.ErrTooManyRecipients
	}
	fingerprint, err := acceptance.Fingerprint(req)
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ent := entity.Entity{CreatedAt: now, UpdatedAt: now}
	evt := &event.Event{Entity: ent, ID: id.NewEventID(), Type: req.Type, TenantID: req.TenantID, ScopeAppID: req.AppID, ScopeOrgID: req.OrgID, Data: append(json.RawMessage{}, req.Data...)}
	receipt := &acceptance.Receipt{Version: acceptance.Version, Producer: req.Producer, InstallationID: req.InstallationID, SourceKey: req.SourceKey, SourceFingerprint: req.SourceFingerprint, AppID: req.AppID, OrgID: req.OrgID, TenantID: req.TenantID, Fingerprint: fingerprint, EventID: evt.ID, AcceptedAt: now, Recipients: []acceptance.Recipient{}}
	ds := make([]*delivery.Delivery, 0, len(endpointIDs))
	for _, epID := range endpointIDs {
		d := &delivery.Delivery{Entity: ent, ID: id.NewDeliveryID(), EventID: evt.ID, EndpointID: epID, EventType: evt.Type, TenantID: evt.TenantID, State: delivery.StatePending, MaxAttempts: maxAttempts, NextAttemptAt: now}
		ds = append(ds, d)
		receipt.Recipients = append(receipt.Recipients, acceptance.Recipient{EndpointID: epID, DeliveryID: d.ID})
	}
	return evt, ds, receipt, nil
}
