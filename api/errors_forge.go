package api

import (
	"errors"
	"net/http"

	"github.com/xraph/forge"

	"github.com/xraph/relay"
)

// mapError converts relay sentinel errors to Forge HTTP errors.
func mapError(err error) error {
	switch {
	case errors.Is(err, relay.ErrEndpointNotFound):
		return forge.NotFound(err.Error())
	case errors.Is(err, relay.ErrEventTypeNotFound):
		return forge.NotFound(err.Error())
	case errors.Is(err, relay.ErrEventNotFound):
		return forge.NotFound(err.Error())
	case errors.Is(err, relay.ErrDeliveryNotFound):
		return forge.NotFound(err.Error())
	case errors.Is(err, relay.ErrDLQNotFound):
		return forge.NotFound(err.Error())
	case errors.Is(err, relay.ErrEventTypeDeprecated):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, relay.ErrPayloadValidationFailed):
		return forge.BadRequest(err.Error())
	case errors.Is(err, relay.ErrDuplicateIdempotencyKey):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, relay.ErrEndpointDisabled):
		return forge.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, relay.ErrNoStore):
		return forge.InternalError(err)
	case errors.Is(err, relay.ErrStoreClosed):
		return forge.InternalError(err)
	case errors.Is(err, relay.ErrMigrationFailed):
		return forge.InternalError(err)
	default:
		return forge.InternalError(err)
	}
}
