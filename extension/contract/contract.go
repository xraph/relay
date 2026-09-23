// Package contract wires relay into the Forge dashboard's contract path. It
// registers the `relay` contributor with the dashboard's contract registry and
// answers its intents from the live Relay instance.
//
// Relay's templ dashboard still renders server-side for now. This package is
// the parallel surface the React shell reads, and it will outlive the templ
// one.
package contract

import (
	"bytes"
	_ "embed"
	"fmt"

	"github.com/xraph/forge/extensions/dashboard/contract"
	"github.com/xraph/forge/extensions/dashboard/contract/dispatcher"
	"github.com/xraph/forge/extensions/dashboard/contract/loader"

	"github.com/xraph/relay"
)

//go:embed manifest.yaml
var manifestYAML []byte

// ContributorName is the join key between this contract and
// packages/plugin-relay's `extension` field, and matches
// extension.ExtensionName. A mismatch hides the React plugin with no error
// anywhere, because that is what an uninstalled extension looks like.
const ContributorName = "relay"

// Deps bundles what the contract handlers need at registration time.
type Deps struct {
	// Relay is the live Relay instance. Required.
	Relay *relay.Relay
}

// Register loads the embedded manifest, validates it, registers the `relay`
// contributor with reg, and binds the handlers against deps.
func Register(
	d *dispatcher.Dispatcher,
	reg contract.Registry,
	wreg contract.WardenRegistry,
	deps Deps,
) error {
	if deps.Relay == nil {
		return fmt.Errorf("relay/contract: Relay is required")
	}

	m, err := loader.Load(bytes.NewReader(manifestYAML), "relay/contract/manifest.yaml")
	if err != nil {
		return fmt.Errorf("relay/contract: load manifest: %w", err)
	}
	if err := loader.Validate(m, wreg); err != nil {
		return fmt.Errorf("relay/contract: validate manifest: %w", err)
	}
	if err := reg.Register(m); err != nil {
		return fmt.Errorf("relay/contract: register manifest: %w", err)
	}

	const c = ContributorName
	for _, bind := range []func() error{
		func() error { return dispatcher.RegisterQuery(d, c, "endpoints.list", 1, endpointsListHandler(deps)) },
		func() error {
			return dispatcher.RegisterQuery(d, c, "endpoints.detail", 1, endpointsDetailHandler(deps))
		},
		func() error {
			return dispatcher.RegisterQuery(d, c, "endpoints.resolve", 1, endpointsResolveHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "endpoints.create", 1, endpointsCreateHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "endpoints.update", 1, endpointsUpdateHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "endpoints.delete", 1, endpointsDeleteHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "endpoints.setEnabled", 1, endpointsSetEnabledHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "endpoints.rotateSecret", 1, endpointsRotateSecretHandler(deps))
		},
	} {
		if err := bind(); err != nil {
			return fmt.Errorf("relay/contract: register intent: %w", err)
		}
	}
	return nil
}
