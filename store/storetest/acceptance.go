package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	gu "github.com/xraph/go-utils/metrics"

	"github.com/xraph/relay/observability"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store"
)

// AcceptanceBackend is the production acceptance capability and its read stores.
type AcceptanceBackend interface {
	store.Store
	acceptance.Store
}

// RunAcceptanceSuite exercises recovery through separate Relay instances sharing storage.
func RunAcceptanceSuite(t *testing.T, s AcceptanceBackend) {
	t.Helper()
	ctx := context.Background()
	et := &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test.accepted"}}
	if err := s.RegisterType(ctx, et); err != nil {
		t.Fatal(err)
	}
	req := acceptance.Request{Producer: "suite", InstallationID: "one", SourceKey: "key", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", OrgID: "org", TenantID: "tenant", Type: "test.accepted", Data: []byte(`{"n":9007199254740993}`)}
	for i, scope := range [][3]string{{"app", "org", "tenant"}, {"app", "org", "tenant"}, {"other", "org", "tenant"}, {"app", "other", "tenant"}, {"app", "org", "other"}, {"", "", "tenant"}} {
		ep := &endpoint.Endpoint{Entity: entity.New(), ID: id.NewEndpointID(), ScopeAppID: scope[0], ScopeOrgID: scope[1], TenantID: scope[2], URL: fmt.Sprintf("https://example.com/%d", i), EventTypes: []string{"*"}, Enabled: true}
		if err := s.CreateEndpoint(ctx, ep); err != nil {
			t.Fatal(err)
		}
		roundtrip, err := s.GetEndpoint(ctx, ep.ID)
		if err != nil {
			t.Fatal(err)
		}
		if roundtrip.ScopeAppID != ep.ScopeAppID || roundtrip.ScopeOrgID != ep.ScopeOrgID {
			t.Fatal("endpoint scope lost")
		}
	}
	metrics := observability.NewMetrics(gu.NewMetricsCollector("acceptance-suite"))
	first, err := relay.New(relay.WithStore(s), relay.WithMetrics(metrics))
	if err != nil {
		t.Fatal(err)
	}
	second, err := relay.New(relay.WithStore(s), relay.WithMetrics(metrics))
	if err != nil {
		t.Fatal(err)
	}
	engines := []*relay.Relay{first, second}
	var wg sync.WaitGroup
	receipts := make(chan *acceptance.Receipt, 20)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, sendErr := engines[i%2].SendReliable(ctx, req)
			receipts <- r
			errs <- sendErr
		}(i)
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original *acceptance.Receipt
	for r := range receipts {
		if original == nil {
			original = r
		}
		if !reflect.DeepEqual(original, r) {
			t.Fatal("duplicate receipt changed")
		}
	}
	if metrics.EventsSentTotal.Value() != 1 || metrics.PendingDeliveries.Value() != 2 {
		t.Fatalf("concurrent acceptance metrics: events=%v pending=%v", metrics.EventsSentTotal.Value(), metrics.PendingDeliveries.Value())
	}
	if len(original.Recipients) != 2 {
		t.Fatalf("recipients = %d", len(original.Recipients))
	}
	ds, err := s.ListByEvent(ctx, original.EventID)
	if err != nil || len(ds) != 2 {
		t.Fatalf("deliveries: %d %v", len(ds), err)
	}
	for _, field := range []string{"app", "org", "tenant", "data", "source fingerprint", "type"} {
		changed := req
		switch field {
		case "app":
			changed.AppID = "other"
		case "org":
			changed.OrgID = "other"
		case "tenant":
			changed.TenantID = "other"
		case "data":
			changed.Data = []byte(`{"n":9007199254740992}`)
		case "source fingerprint":
			changed.SourceFingerprint = strings.Repeat("b", 64)
		case "type":
			changed.Type = "unknown"
		}
		r, sendErr := engines[0].SendReliable(ctx, changed)
		if !errors.Is(sendErr, acceptance.ErrConflict) || r != nil {
			t.Fatalf("%s conflict: %v %v", field, r, sendErr)
		}
	}
	// Different installations can reuse source keys, and empty fanout is acceptance.
	other := req
	other.InstallationID = "two"
	other.TenantID = "empty"
	r, err := engines[0].SendReliable(ctx, other)
	if err != nil || len(r.Recipients) != 0 || r.EventID == original.EventID {
		t.Fatalf("independent installation: %v %v", r, err)
	}
	// Concurrent different first contents select one winner without disclosure.
	conflict := req
	conflict.SourceKey = "race-conflict"
	a := conflict
	b := conflict
	b.Data = []byte(`2`)
	wins := make(chan *acceptance.Receipt, 2)
	failures := make(chan error, 2)
	for _, request := range []acceptance.Request{a, b} {
		wg.Add(1)
		go func(q acceptance.Request) {
			defer wg.Done()
			r, e := engines[0].SendReliable(ctx, q)
			wins <- r
			failures <- e
		}(request)
	}
	wg.Wait()
	close(wins)
	close(failures)
	count := 0
	for r := range wins {
		if r != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("conflicting winners: %d", count)
	}
	for e := range failures {
		if e != nil && !errors.Is(e, acceptance.ErrConflict) {
			t.Fatal(e)
		}
	}

	uncertain := req
	uncertain.SourceKey = "lost-ack"
	wrapper := &acceptanceLostAck{AcceptanceBackend: s}
	uncertainEngine, err := relay.New(relay.WithStore(wrapper))
	if err != nil {
		t.Fatal(err)
	}
	if r, sendErr := uncertainEngine.SendReliable(ctx, uncertain); sendErr == nil || r != nil {
		t.Fatal("expected unknown outcome")
	}
	recoveredAck, err := uncertainEngine.SendReliable(ctx, uncertain)
	if err != nil || !reflect.DeepEqual(recoveredAck, wrapper.committed) {
		t.Fatal("lost acknowledgement recovery", err)
	}
	for _, recipient := range original.Recipients {
		if deleteErr := s.DeleteEndpoint(ctx, recipient.EndpointID); deleteErr != nil {
			t.Fatal(deleteErr)
		}
	}
	if deleteErr := s.DeleteType(ctx, req.Type); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	recovered, err := engines[1].SendReliable(ctx, req)
	if err != nil || !reflect.DeepEqual(original, recovered) {
		t.Fatalf("recover after catalog/endpoint deletion: %v %v", recovered, err)
	}
	// Receipt data returned to callers must not alias stored state.
	recovered.Recipients[0].DeliveryID = id.NewDeliveryID()
	again, err := engines[0].SendReliable(ctx, req)
	if err != nil || !reflect.DeepEqual(original, again) {
		t.Fatal("receipt mutated")
	}
}

// acceptanceLostAck discards the acknowledgement after the real backend commits.
type acceptanceLostAck struct {
	AcceptanceBackend
	committed *acceptance.Receipt
}

func (s *acceptanceLostAck) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	r, err := s.AcceptanceBackend.AcceptEvent(ctx, req, maxAttempts)
	if err != nil {
		return nil, err
	}
	if s.committed == nil {
		s.committed = r
		return nil, errors.New("lost commit acknowledgement")
	}
	return r, nil
}
