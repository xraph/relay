package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/xraph/relay/delivery"
)

// ListDeliveries returns one page of the delivery log, newest first.
func (s *Store) ListDeliveries(ctx context.Context, q delivery.Query) (*delivery.Page, error) {
	limit, pos, err := q.Prepare()
	if err != nil {
		return nil, err
	}

	var (
		conds []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return placeholder(len(args))
	}
	if q.State != nil {
		conds = append(conds, "state = "+arg(string(*q.State)))
	}
	if q.EndpointID != nil {
		conds = append(conds, "endpoint_id = "+arg(q.EndpointID.String()))
	}
	if q.EventID != nil {
		conds = append(conds, "event_id = "+arg(q.EventID.String()))
	}
	if q.EventType != "" {
		conds = append(conds, "event_type = "+arg(q.EventType))
	}
	if q.TenantID != "" {
		conds = append(conds, "tenant_id = "+arg(q.TenantID))
	}
	if lo, hi, ok := q.StatusClass.Range(); ok {
		conds = append(conds, fmt.Sprintf("last_status_code BETWEEN %s AND %s", arg(lo), arg(hi)))
	}
	if q.From != nil {
		conds = append(conds, "created_at >= "+arg(q.From.UTC()))
	}
	if q.To != nil {
		conds = append(conds, "created_at <= "+arg(q.To.UTC()))
	}
	if pos != nil {
		// The id breaks ties between rows created in the same instant.
		at := pos.CreatedAt.UTC()
		conds = append(conds, fmt.Sprintf("(created_at < %s OR (created_at = %s AND id < %s))",
			arg(at), arg(at), arg(pos.ID)))
	}

	var models []deliveryModel
	sel := s.sdb.NewSelect(&models)
	if len(conds) > 0 {
		sel = sel.Where(strings.Join(conds, " AND "), args...)
	}
	// One extra row says whether there is a next page.
	if err := sel.OrderExpr("created_at DESC, id DESC").Limit(limit + 1).Scan(ctx); err != nil {
		return nil, err
	}

	page := &delivery.Page{Deliveries: make([]*delivery.Delivery, 0, min(len(models), limit)), Complete: true}
	for i := range models {
		if i == limit {
			page.NextCursor = delivery.CursorFor(page.Deliveries[limit-1])
			break
		}
		d, err := fromDeliveryModel(&models[i])
		if err != nil {
			return nil, err
		}
		page.Deliveries = append(page.Deliveries, d)
	}
	return page, nil
}

func placeholder(int) string { return "?" }
