package catalog

import "encoding/json"

// WebhookDefinition is the canonical description of a webhook event type.
// It is the unit of Relay's dynamic catalog. Definitions are stored in the
// database and can be registered at boot, via API, or by other Forge extensions.
type WebhookDefinition struct {
	// Name is the dot-separated event type name.
	// Convention: "<resource>.<action>" — e.g. "invoice.created", "deployment.completed".
	Name string `json:"name"`

	// Description is a human-readable explanation of when this event fires.
	Description string `json:"description"`

	// Group is an optional category for organizing event types in docs/UI.
	Group string `json:"group,omitempty"`

	// Schema is an optional JSON Schema (draft-07) describing the payload shape.
	// When set, relay.Send() validates the event data against this schema.
	Schema json.RawMessage `json:"schema,omitempty"`

	// SchemaVersion tracks changes to the Schema itself.
	SchemaVersion string `json:"schema_version,omitempty"`

	// Version is the API version of this event type.
	// Convention: date-based, e.g. "2025-01-01".
	Version string `json:"version"`

	// Example is an optional example payload for documentation and testing.
	Example json.RawMessage `json:"example,omitempty"`
}
