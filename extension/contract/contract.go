// Package contract wires relay into the Forge dashboard's contract path. It
// registers the `relay` contributor with the dashboard's contract registry and
// answers its intents from the live Relay instance.
//
// It is the only dashboard surface Relay has. The templ dashboard it replaced
// is gone; MIGRATION.md records where each of its pages went.
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

		func() error { return dispatcher.RegisterQuery(d, c, "deliveries.list", 1, deliveriesListHandler(deps)) },
		func() error {
			return dispatcher.RegisterQuery(d, c, "deliveries.detail", 1, deliveriesDetailHandler(deps))
		},

		func() error { return dispatcher.RegisterQuery(d, c, "events.list", 1, eventsListHandler(deps)) },
		func() error { return dispatcher.RegisterQuery(d, c, "events.detail", 1, eventsDetailHandler(deps)) },
		func() error { return dispatcher.RegisterCommand(d, c, "events.send", 1, eventsSendHandler(deps)) },

		func() error { return dispatcher.RegisterQuery(d, c, "eventTypes.list", 1, eventTypesListHandler(deps)) },
		func() error {
			return dispatcher.RegisterQuery(d, c, "eventTypes.detail", 1, eventTypesDetailHandler(deps))
		},
		func() error {
			return dispatcher.RegisterQuery(d, c, "eventTypes.match", 1, eventTypesMatchHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "eventTypes.register", 1, eventTypesRegisterHandler(deps))
		},
		func() error {
			return dispatcher.RegisterCommand(d, c, "eventTypes.deprecate", 1, eventTypesDeprecateHandler(deps))
		},

		func() error { return dispatcher.RegisterQuery(d, c, "dlq.list", 1, dlqListHandler(deps)) },
		func() error { return dispatcher.RegisterQuery(d, c, "dlq.detail", 1, dlqDetailHandler(deps)) },
		func() error { return dispatcher.RegisterQuery(d, c, "dlq.bulkPreview", 1, dlqBulkPreviewHandler(deps)) },
		func() error { return dispatcher.RegisterCommand(d, c, "dlq.replay", 1, dlqReplayHandler(deps)) },
		func() error { return dispatcher.RegisterCommand(d, c, "dlq.replayBulk", 1, dlqReplayBulkHandler(deps)) },
		func() error { return dispatcher.RegisterCommand(d, c, "dlq.purge", 1, dlqPurgeHandler(deps)) },

		func() error { return dispatcher.RegisterQuery(d, c, "overview.stats", 1, overviewStatsHandler(deps)) },
		func() error { return dispatcher.RegisterQuery(d, c, "settings.config", 1, settingsConfigHandler(deps)) },
	} {
		if err := bind(); err != nil {
			return fmt.Errorf("relay/contract: register intent: %w", err)
		}
	}
	return nil
}
