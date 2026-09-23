package extension

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"

	"github.com/xraph/relay"
	"github.com/xraph/relay/store/memory"
)

// The dashboard finds relay's contract by interface. If this method's
// signature drifted from dashboard.ContractContributorAware, the dashboard
// would simply never call it and the React plugin would render nothing, with
// no error anywhere. The compile-time assertion in extension.go guards the
// signature; this guards that calling it actually wires the intents up.
func TestRegisterContractContributorWiresTheIntents(t *testing.T) {
	r, err := relay.New(relay.WithStore(memory.New()))
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	// Built the way production builds it, then given a relay instance, since
	// Register (which normally creates one) needs a whole Forge app.
	e := New()
	e.r = r
	disp := dispatcher.New(nil)
	if regErr := e.RegisterContractContributor(disp, contract.NewRegistry(), contract.NewWardenRegistry()); regErr != nil {
		t.Fatalf("RegisterContractContributor: %v", regErr)
	}

	out, _, err := disp.Dispatch(context.Background(), contract.Request{
		Envelope: "v1", Kind: contract.KindQuery, Contributor: "relay",
		Intent: "endpoints.list", IntentVersion: 1, Payload: json.RawMessage(`{}`),
	}, contract.Principal{})
	if err != nil {
		t.Fatalf("endpoints.list after registration: %v", err)
	}
	var got struct {
		Endpoints []any `json:"endpoints"`
	}
	if err := json.Unmarshal(out, &got); err != nil || got.Endpoints == nil {
		t.Fatalf("endpoints.list returned %s, want an endpoints array", out)
	}
}

// Before relay is initialised there is nothing to wire. That must be a quiet
// skip, not a panic that takes the dashboard down with it. New() is how
// production builds the extension: its logger is still nil until Register runs,
// which is the case the method has to survive. (A bare &Extension{} has a nil
// embedded *BaseExtension, which production never produces, so it is not used.)
func TestRegisterContractContributorSkipsWithoutRelay(t *testing.T) {
	e := New()
	if err := e.RegisterContractContributor(dispatcher.New(nil), contract.NewRegistry(),
		contract.NewWardenRegistry()); err != nil {
		t.Fatalf("with no relay instance: %v, want nil", err)
	}
}
