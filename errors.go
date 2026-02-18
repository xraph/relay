package relay

import "errors"

// Sentinel errors returned by Relay operations.
var (
	// ErrNoStore is returned when a Relay is created without a store.
	ErrNoStore = errors.New("relay: store is required")

	// ErrEndpointNotFound is returned when an endpoint cannot be found.
	ErrEndpointNotFound = errors.New("relay: endpoint not found")

	// ErrEventTypeNotFound is returned when an event type is not registered in the catalog.
	ErrEventTypeNotFound = errors.New("relay: event type not found")

	// ErrEventTypeDeprecated is returned when sending an event with a deprecated type.
	ErrEventTypeDeprecated = errors.New("relay: event type is deprecated")

	// ErrPayloadValidationFailed is returned when event data fails JSON Schema validation.
	ErrPayloadValidationFailed = errors.New("relay: payload validation failed")

	// ErrDuplicateIdempotencyKey is returned when an event with the same idempotency key already exists.
	ErrDuplicateIdempotencyKey = errors.New("relay: duplicate idempotency key")

	// ErrEndpointDisabled is returned when attempting to deliver to a disabled endpoint.
	ErrEndpointDisabled = errors.New("relay: endpoint is disabled")

	// ErrStoreClosed is returned when a store operation is attempted after the store is closed.
	ErrStoreClosed = errors.New("relay: store is closed")

	// ErrMigrationFailed is returned when a database migration fails.
	ErrMigrationFailed = errors.New("relay: migration failed")

	// ErrDLQNotFound is returned when a DLQ entry cannot be found.
	ErrDLQNotFound = errors.New("relay: dlq entry not found")

	// ErrDeliveryNotFound is returned when a delivery cannot be found.
	ErrDeliveryNotFound = errors.New("relay: delivery not found")

	// ErrEventNotFound is returned when an event cannot be found.
	ErrEventNotFound = errors.New("relay: event not found")
)
