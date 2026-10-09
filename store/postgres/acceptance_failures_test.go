package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
)

func TestReliableAcceptanceRollbackAndRetention(t *testing.T) {
	s := openPgStore(t, startPostgres(t))
	ctx := context.Background()
	pg := pgdriver.Unwrap(s.DB())
	exec := func(query string) {
		t.Helper()
		if _, err := pg.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	req := acceptance.Request{Producer: "producer", InstallationID: "installation", SourceKey: "rollback", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", OrgID: "org", TenantID: "tenant", Type: "test", Data: []byte(`{"integer":9007199254740993}`)}
	if err := s.RegisterType(ctx, &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.CreateEndpoint(ctx, &endpoint.Endpoint{Entity: entity.New(), ID: id.NewEndpointID(), ScopeAppID: "app", ScopeOrgID: "org", TenantID: "tenant", Enabled: true, EventTypes: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
	}
	empty := func() {
		t.Helper()
		for _, table := range []string{"relay_events", "relay_deliveries", "relay_acceptances"} {
			var n int
			if err := pg.NewRaw("SELECT count(*) FROM "+table).Scan(ctx, &n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("%s has %d partial rows", table, n)
			}
		}
	}
	for _, stage := range []string{"resolve", "event", "delivery1", "delivery2", "receipt", "commit"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "resolve" {
				exec("ALTER TABLE relay_endpoints RENAME TO relay_endpoints_unavailable")
				_, err := s.AcceptEvent(ctx, req, 3)
				exec("ALTER TABLE relay_endpoints_unavailable RENAME TO relay_endpoints")
				if err == nil {
					t.Fatal("expected resolution failure")
				}
				empty()
				return
			}
			table := "relay_events"
			condition := "TRUE"
			deferred := ""
			switch stage {
			case "delivery1":
				table = "relay_deliveries"
			case "delivery2":
				table = "relay_deliveries"
				condition = "(SELECT count(*) FROM relay_deliveries) = 1"
			case "receipt":
				table = "relay_acceptances"
			case "commit":
				deferred = "CONSTRAINT "
			}
			exec(fmt.Sprintf(`CREATE FUNCTION relay_fail_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'injected acceptance failure'; END IF; RETURN NEW; END $$`, condition))
			if stage == "commit" {
				exec("CREATE " + deferred + "TRIGGER acceptance_fault AFTER INSERT ON relay_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION relay_fail_acceptance()")
			} else {
				exec("CREATE TRIGGER acceptance_fault BEFORE INSERT ON " + table + " FOR EACH ROW EXECUTE FUNCTION relay_fail_acceptance()")
			}
			receipt, err := s.AcceptEvent(ctx, req, 3)
			exec("DROP TRIGGER acceptance_fault ON " + table)
			exec("DROP FUNCTION relay_fail_acceptance()")
			if err == nil || receipt != nil {
				t.Fatal("expected failed acceptance")
			}
			empty()
		})
	}
	original, err := s.AcceptEvent(ctx, req, 3)
	if err != nil {
		t.Fatal(err)
	}
	// The persisted receipt is independent of event payload and delivery retention.
	exec("DELETE FROM relay_events")
	exec("DELETE FROM relay_deliveries")
	exec("DELETE FROM relay_endpoints")
	exec("DELETE FROM relay_event_types")
	recovered, err := s.AcceptEvent(ctx, req, 3)
	if err != nil || !reflect.DeepEqual(original, recovered) {
		t.Fatalf("retention recovery: %v %v", recovered, err)
	}
	events, err := s.ListEvents(ctx, event.ListOpts{})
	if err != nil || len(events) != 0 {
		t.Fatal("retry recreated payload", err)
	}
	changed := req
	changed.OrgID = "another"
	if r, err := s.AcceptEvent(ctx, changed, 3); !errors.Is(err, acceptance.ErrConflict) || r != nil {
		t.Fatal("scope conflict disclosed retained receipt", err)
	}
}

func TestReliableAcceptancePinsMembershipDuringChanges(t *testing.T) {
	s := openPgStore(t, startPostgres(t))
	ctx := context.Background()
	pg := pgdriver.Unwrap(s.DB())
	req := acceptance.Request{Producer: "producer", InstallationID: "installation", SourceKey: "membership", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test", Data: []byte(`1`)}
	if err := s.RegisterType(ctx, &catalog.EventType{Entity: entity.New(), ID: id.NewEventTypeID(), Definition: catalog.WebhookDefinition{Name: "test"}}); err != nil {
		t.Fatal(err)
	}
	ep := &endpoint.Endpoint{Entity: entity.New(), ID: id.NewEndpointID(), ScopeAppID: "app", TenantID: "tenant", Enabled: true, EventTypes: []string{"*"}}
	if err := s.CreateEndpoint(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Exec(ctx, `CREATE FUNCTION relay_pause_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(8976543); RETURN NEW; END $$;
CREATE TRIGGER acceptance_pause BEFORE INSERT ON relay_events FOR EACH ROW EXECUTE FUNCTION relay_pause_acceptance()`); err != nil {
		t.Fatal(err)
	}
	gate, err := pg.BeginTxQuery(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gate.Rollback() }()
	if _, err = gate.NewRaw("SELECT pg_advisory_xact_lock(8976543)").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	result := make(chan *acceptance.Receipt, 1)
	failure := make(chan error, 1)
	go func() { r, e := s.AcceptEvent(ctx, req, 3); result <- r; failure <- e }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err = pg.NewRaw("SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=8976543 AND NOT granted").Scan(ctx, &waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acceptance did not reach post-selection barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = s.DeleteEndpoint(ctx, ep.ID); err != nil {
		t.Fatal(err)
	}
	replacement := *ep
	replacement.ID = id.NewEndpointID()
	if err = s.CreateEndpoint(ctx, &replacement); err != nil {
		t.Fatal(err)
	}
	if err = gate.Commit(); err != nil {
		t.Fatal(err)
	}
	receipt := <-result
	if err = <-failure; err != nil {
		t.Fatal(err)
	}
	if len(receipt.Recipients) != 1 || receipt.Recipients[0].EndpointID != ep.ID {
		t.Fatal("membership changed after selection")
	}
	retry, err := s.AcceptEvent(ctx, req, 3)
	if err != nil || !reflect.DeepEqual(receipt, retry) {
		t.Fatal("retry appended replacement endpoint", err)
	}
}
