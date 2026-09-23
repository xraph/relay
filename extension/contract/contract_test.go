package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"

	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	relaycontract "github.com/xraph/relay/extension/contract"
	"github.com/xraph/relay/id"
	"github.com/xraph/relay/internal/entity"
	"github.com/xraph/relay/store/memory"
)

// harness registers the relay contract against a real dispatcher and
// registry, the same path the dashboard uses. Tests dispatch envelopes and
// read the raw JSON back, so they check the field names the React client
// actually receives, not a Go struct that might be tagged differently.
type harness struct {
	t     *testing.T
	disp  *dispatcher.Dispatcher
	r     *relay.Relay
	store *memory.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store := memory.New()
	r, err := relay.New(relay.WithStore(store))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	disp := dispatcher.New(nil)
	if err := relaycontract.Register(disp, contract.NewRegistry(), contract.NewWardenRegistry(),
		relaycontract.Deps{Relay: r}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return &harness{t: t, disp: disp, r: r, store: store}
}

// call sends input the way the React client does: a query carries it in
// Params, a command in Payload. The dispatcher decodes both, but through
// different paths (Params is a map marshalled back to JSON), and a test that
// only ever used Payload would never exercise the one every query takes.
func (h *harness) call(kind contract.Kind, intent string, input any) (map[string]any, error) {
	h.t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		h.t.Fatalf("marshal input: %v", err)
	}
	req := contract.Request{
		Envelope:      "v1",
		Kind:          kind,
		Contributor:   relaycontract.ContributorName,
		Intent:        intent,
		IntentVersion: 1,
	}
	if kind == contract.KindQuery {
		var params map[string]any
		if uerr := json.Unmarshal(raw, &params); uerr != nil {
			h.t.Fatalf("query input must be a JSON object: %v", uerr)
		}
		req.Params = params
	} else {
		req.Payload = raw
	}
	out, _, err := h.disp.Dispatch(context.Background(), req, contract.Principal{})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if uerr := json.Unmarshal(out, &m); uerr != nil {
		h.t.Fatalf("%s returned non-object JSON %s: %v", intent, out, uerr)
	}
	return m, nil
}

func (h *harness) query(intent string, payload any) map[string]any {
	h.t.Helper()
	m, err := h.call(contract.KindQuery, intent, payload)
	if err != nil {
		h.t.Fatalf("%s: %v", intent, err)
	}
	return m
}

func (h *harness) command(intent string, payload any) map[string]any {
	h.t.Helper()
	m, err := h.call(contract.KindCommand, intent, payload)
	if err != nil {
		h.t.Fatalf("%s: %v", intent, err)
	}
	return m
}

func (h *harness) create(tenant, description string) string {
	h.t.Helper()
	ep, err := h.r.Endpoints().Create(context.Background(), endpoint.Input{
		TenantID:    tenant,
		URL:         "https://receiver.example/hook",
		Description: description,
		EventTypes:  []string{"invoice.*"},
	})
	if err != nil {
		h.t.Fatalf("create: %v", err)
	}
	return ep.ID.String()
}

func codeOf(err error) contract.ErrorCode {
	var ce *contract.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ContributorName is the join key with packages/plugin-relay. A mismatch
// hides the whole React plugin with no error anywhere.
func TestContributorNameIsRelay(t *testing.T) {
	if relaycontract.ContributorName != "relay" {
		t.Fatalf("ContributorName = %q, want \"relay\"", relaycontract.ContributorName)
	}
}

// Every intent the manifest declares has a handler. Adding one to the yaml
// without a handler is found here, not by a 404 in the browser.
func TestEveryDeclaredIntentIsRegistered(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		kind   contract.Kind
		intent string
	}{
		{contract.KindQuery, "endpoints.list"},
		{contract.KindQuery, "endpoints.detail"},
		{contract.KindQuery, "endpoints.resolve"},
		{contract.KindCommand, "endpoints.create"},
		{contract.KindCommand, "endpoints.update"},
		{contract.KindCommand, "endpoints.delete"},
		{contract.KindCommand, "endpoints.setEnabled"},
		{contract.KindCommand, "endpoints.rotateSecret"},
	} {
		_, err := h.call(tc.kind, tc.intent, map[string]any{})
		// A handler may reject an empty payload; what must not happen is the
		// dispatcher not knowing the intent at all.
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "not registered") {
			t.Errorf("%s is not registered: %v", tc.intent, err)
		}
	}
}

// The list's wire shape is what the React page reads. camelCase, and the
// fields the page depends on present by these exact names.
func TestEndpointsListWireShape(t *testing.T) {
	h := newHarness(t)
	h.create("acme", "prod")

	out := h.query("endpoints.list", map[string]any{"tenantId": "acme"})
	eps, ok := out["endpoints"].([]any)
	if !ok || len(eps) != 1 {
		t.Fatalf("endpoints.list = %s, want an \"endpoints\" array of one", asJSON(out))
	}
	row := eps[0].(map[string]any)
	for _, key := range []string{"id", "tenantId", "url", "eventTypes", "enabled", "signed", "createdAt"} {
		if _, ok := row[key]; !ok {
			t.Errorf("list row is missing %q: %s", key, asJSON(row))
		}
	}
	if row["tenantId"] != "acme" {
		t.Errorf("tenantId = %v, want acme", row["tenantId"])
	}
}

// An empty tenant lists every tenant, matching ListEndpoints since it was
// fixed. The handler must pass it straight through, not re-derive it.
func TestEndpointsListEmptyTenantListsEveryTenant(t *testing.T) {
	h := newHarness(t)
	a := h.create("acme", "")
	b := h.create("globex", "")

	out := h.query("endpoints.list", map[string]any{})
	ids := map[string]bool{}
	for _, e := range out["endpoints"].([]any) {
		ids[e.(map[string]any)["id"].(string)] = true
	}
	if !ids[a] || !ids[b] {
		t.Fatalf("an empty tenant did not list both tenants' endpoints: %s", asJSON(out))
	}
}

// An endpoint with no secret still gets a well-formed signature header from
// a server that has not been upgraded, so nothing on the row would tell an
// operator. The list has to.
func TestEndpointsListReportsAnUnsignedEndpoint(t *testing.T) {
	h := newHarness(t)
	ep := &endpoint.Endpoint{
		Entity: entity.New(), ID: id.NewEndpointID(), TenantID: "acme",
		URL: "https://unsigned.example/hook", EventTypes: []string{"*"}, Enabled: true, Secret: "",
	}
	if err := h.store.CreateEndpoint(context.Background(), ep); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out := h.query("endpoints.list", map[string]any{"tenantId": "acme"})
	row := out["endpoints"].([]any)[0].(map[string]any)
	if row["signed"] != false {
		t.Fatalf("signed = %v for an endpoint with no secret, want false", row["signed"])
	}
}

// No read may ever carry a signing secret.
func TestNoReadCarriesTheSecret(t *testing.T) {
	h := newHarness(t)
	epID := h.create("acme", "")
	list := asJSON(h.query("endpoints.list", map[string]any{"tenantId": "acme"}))
	detail := asJSON(h.query("endpoints.detail", map[string]any{"id": epID}))
	for name, body := range map[string]string{"list": list, "detail": detail} {
		if strings.Contains(body, "whsec_") {
			t.Fatalf("endpoints.%s put the signing secret on the wire: %s", name, body)
		}
	}
}

// A nil field leaves the value alone; an explicit "" clears it. A non-pointer
// field cannot tell those apart, and endpoint.Service.Update, which guards
// every field with != "", physically cannot clear one.
func TestUpdateClearsOnEmptyAndKeepsOnAbsent(t *testing.T) {
	h := newHarness(t)
	epID := h.create("acme", "keep me")

	h.command("endpoints.update", map[string]any{"id": epID, "url": "https://moved.example/hook"})
	if got := h.query("endpoints.detail", map[string]any{"id": epID})["description"]; got != "keep me" {
		t.Fatalf("an update that did not mention description changed it to %v", got)
	}
	h.command("endpoints.update", map[string]any{"id": epID, "description": ""})
	if got, ok := h.query("endpoints.detail", map[string]any{"id": epID})["description"]; ok && got != "" {
		t.Fatalf("an update setting description to \"\" left it as %v", got)
	}
}

// The rotated secret is returned once, by the command, and no read repeats it.
func TestRotateSecretReturnsItOnce(t *testing.T) {
	h := newHarness(t)
	epID := h.create("acme", "")
	out := h.command("endpoints.rotateSecret", map[string]any{"id": epID})
	secret, _ := out["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("rotateSecret = %s, want a whsec_ secret", asJSON(out))
	}
	if strings.Contains(asJSON(h.query("endpoints.detail", map[string]any{"id": epID})), secret) {
		t.Fatal("a read repeated the rotated secret")
	}
}

// Error codes are what the React page branches on.
func TestErrorsCarryCodesThePageCanBranchOn(t *testing.T) {
	h := newHarness(t)

	if _, err := h.call(contract.KindQuery, "endpoints.detail",
		map[string]any{"id": id.NewEndpointID().String()}); codeOf(err) != contract.CodeNotFound {
		t.Errorf("detail of a missing endpoint: code %q, err %v; want NOT_FOUND", codeOf(err), err)
	}
	if _, err := h.call(contract.KindQuery, "endpoints.detail",
		map[string]any{"id": "not-an-id"}); codeOf(err) != contract.CodeBadRequest {
		t.Errorf("detail of a malformed id: code %q, err %v; want BAD_REQUEST", codeOf(err), err)
	}
	_, err := h.call(contract.KindCommand, "endpoints.create",
		map[string]any{"url": "https://a.example/hook", "eventTypes": []string{"*"}})
	if codeOf(err) != contract.CodeBadRequest {
		t.Fatalf("create without a tenant: code %q, err %v; want BAD_REQUEST", codeOf(err), err)
	}
	var ce *contract.Error
	if !errors.As(err, &ce) {
		t.Fatalf("create without a tenant: %T is not a *contract.Error", err)
	}
	if ce.Details["field"] != "tenant_id" {
		t.Errorf("create without a tenant: details %v, want field tenant_id", ce.Details)
	}
}

// The message is what every client renders, and the dashboard's client keeps
// only the code and the message: details never reach the page. So the message
// has to name the field on its own. "required" alone tells the operator that
// something is missing and not what.
func TestValidationMessagesNameTheirField(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{"no tenant", map[string]any{"url": "https://a.example/hook", "eventTypes": []string{"*"}}, "Tenant ID: required"},
		{"bad url", map[string]any{"tenantId": "t", "url": "not a url", "eventTypes": []string{"*"}}, "URL: invalid URL"},
		{"no event types", map[string]any{"tenantId": "t", "url": "https://a.example/hook", "eventTypes": []string{}},
			"Event types: at least one event type pattern required"},
	}
	for _, tc := range cases {
		_, err := h.call(contract.KindCommand, "endpoints.create", tc.payload)
		var ce *contract.Error
		if !errors.As(err, &ce) {
			t.Fatalf("%s: %v is not a *contract.Error", tc.name, err)
		}
		if ce.Message != tc.want {
			t.Errorf("%s: message %q, want %q", tc.name, ce.Message, tc.want)
		}
	}
}
