package acceptanceutil

import (
	"time"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

// Fanout prepares the legacy Send delivery batch before persistence.
func Fanout(evt *event.Event, endpointIDs []id.ID, maxAttempts int) []*delivery.Delivery {
	ds := make([]*delivery.Delivery, 0, len(endpointIDs))
	for _, epID := range endpointIDs {
		ds = append(ds, &delivery.Delivery{Entity: entity.New(), ID: id.NewDeliveryID(), EventID: evt.ID, EndpointID: epID, EventType: evt.Type, TenantID: evt.TenantID, State: delivery.StatePending, MaxAttempts: maxAttempts, NextAttemptAt: time.Now().UTC()})
	}
	return ds
}
