package memory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

func TestAcceptanceRetentionAndFailures(t *testing.T) {
	s := New()
	ctx := context.Background()
	req := acceptance.Request{Producer: "test", InstallationID: "instance", SourceKey: "source", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test", Data: []byte(`{"a":[1,2]}`)}
	if err := s.RegisterType(ctx, &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.AcceptEvent(cancelled, req, 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(s.events) != 0 || len(s.receipts) != 0 || len(s.deliveries) != 0 {
		t.Fatal("cancelled request persisted")
	}
	for i := 0; i <= acceptance.MaxRecipients; i++ {
		ep := &endpoint.Endpoint{ID: id.NewEndpointID(), ScopeAppID: "app", TenantID: "tenant", EventTypes: []string{"*"}, Enabled: true}
		s.endpoints[ep.ID.String()] = ep
	}
	if _, err := s.AcceptEvent(ctx, req, 3); !errors.Is(err, acceptance.ErrTooManyRecipients) {
		t.Fatal(err)
	}
	if len(s.events) != 0 || len(s.receipts) != 0 || len(s.deliveries) != 0 {
		t.Fatal("overflow persisted")
	}
	s.endpoints = map[string]*endpoint.Endpoint{}
	original, err := s.AcceptEvent(ctx, req, 3)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.events, original.EventID.String())
	recovered, err := s.AcceptEvent(ctx, req, 3)
	if err != nil || !reflect.DeepEqual(original, recovered) {
		t.Fatalf("retained receipt: %v %v", recovered, err)
	}
	if len(s.events) != 0 {
		t.Fatal("retry recreated deleted payload")
	}
}

func TestAcceptanceOwnsMutableInput(t *testing.T) {
	s := New()
	ctx := context.Background()
	et := &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test"}}
	if err := s.RegisterType(ctx, et); err != nil {
		t.Fatal(err)
	}
	ep := &endpoint.Endpoint{ID: id.NewEndpointID(), ScopeAppID: "app", TenantID: "tenant", EventTypes: []string{"*"}, Enabled: true}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	ep.ScopeAppID = "other"
	ep.EventTypes[0] = "no-match"
	et.IsDeprecated = true
	req := acceptance.Request{Producer: "test", InstallationID: "instance", SourceKey: "source", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test", Data: []byte(`{"a":[1,2]}`)}
	receipt, err := s.AcceptEvent(ctx, req, 3)
	if err != nil || len(receipt.Recipients) != 1 {
		t.Fatalf("caller mutation changed stored input: %v %v", receipt, err)
	}
	req.Data[6] = '9'
	stored, err := s.GetEvent(ctx, receipt.EventID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.Data.(json.RawMessage)) != `{"a":[1,2]}` {
		t.Fatal("request payload aliased stored event")
	}
}

func TestAcceptanceSchemaPreservesLargeNumbers(t *testing.T) {
	s := New()
	ctx := context.Background()
	et := &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test", Schema: json.RawMessage(`{"type":"integer","minimum":9007199254740993}`)}}
	if err := s.RegisterType(ctx, et); err != nil {
		t.Fatal(err)
	}
	req := acceptance.Request{Producer: "test", InstallationID: "instance", SourceKey: "source", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test", Data: []byte(`9007199254740992`)}
	if receipt, err := s.AcceptEvent(ctx, req, 3); err == nil || receipt != nil {
		t.Fatal("schema rounded integer minimum")
	}
	req.Data = []byte(`9007199254740993`)
	if _, err := s.AcceptEvent(ctx, req, 3); err != nil {
		t.Fatal(err)
	}
}
