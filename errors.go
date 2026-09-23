package relay

import (
	"errors"

	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/internal/cursor"
	"github.com/xraph/relay/internal/errs"
)

// Sentinel errors returned by Relay operations.
var (
	// ErrNoStore is returned when a Relay is created without a store.
	ErrNoStore = errors.New("relay: store is required")

	// ErrEndpointNotFound is returned when an endpoint cannot be found.
	ErrEndpointNotFound = errs.ErrEndpointNotFound

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

	// ErrAlreadyReplayed is returned when a DLQ entry that has already been
	// replayed is replayed again. Replaying re-sends a real webhook, so the
	// second call is refused rather than silently duplicating the delivery.
	//
	// It is defined in the dlq package and aliased here, because dlq is where
	// replay happens and dlq cannot import relay without a cycle. The two
	// names are the same value, so errors.Is works with either.
	ErrAlreadyReplayed = dlq.ErrAlreadyReplayed

	// ErrDeliveryNotFound is returned when a delivery cannot be found.
	ErrDeliveryNotFound = errors.New("relay: delivery not found")

	// ErrEventNotFound is returned when an event cannot be found.
	ErrEventNotFound = errs.ErrEventNotFound

	// ErrInvalidCursor is returned for a list cursor this store did not
	// issue. It is never read as "start from the top": a client that sent a
	// bad cursor would get the first page again and think it was the next.
	ErrInvalidCursor = cursor.ErrInvalid

	// ErrInvalidFilter is returned for a list filter value the store does
	// not recognise, such as an unknown delivery status class.
	ErrInvalidFilter = errs.ErrInvalidFilter
)
