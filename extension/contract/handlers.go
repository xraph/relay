package contract

import (
	"errors"
	"strings"

	"github.com/xraph/forge/extensions/dashboard/contract"

	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
	redisstore "github.com/xraph/relay/store/redis"
)

// AckResponse is what a command that has nothing else to say returns. ID names
// the record acted on, so the client can key a toast without a second read.
type AckResponse struct {
	OK bool   `json:"ok"`
	ID string `json:"id,omitempty"`
}

// parseEndpointID turns a wire string into an endpoint id, answering with a
// contract error so the client gets a code it can branch on.
func parseEndpointID(raw string) (id.ID, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return id.ID{}, &contract.Error{Code: contract.CodeBadRequest, Message: "endpoint id is required"}
	}
	parsed, err := id.ParseEndpointID(trimmed)
	if err != nil {
		return id.ID{}, &contract.Error{Code: contract.CodeBadRequest, Message: "malformed endpoint id"}
	}
	return parsed, nil
}

// mapRelayError turns a domain error into a contract error. The field a
// validation error is about goes in Details, since contract.Error has no field
// of its own for it. Anything unrecognised becomes Internal.
func mapRelayError(err error) error {
	if err == nil {
		return nil
	}
	var verr *endpoint.ValidationError
	if errors.As(err, &verr) {
		// The dashboard client keeps the code and the message and drops
		// details, so the message names the field on its own. Details stays
		// for clients that do read it.
		return &contract.Error{
			Code:    contract.CodeBadRequest,
			Message: fieldLabel(verr.Field) + ": " + verr.Message,
			Details: map[string]any{"field": verr.Field},
		}
	}
	if errors.Is(err, relay.ErrEndpointNotFound) {
		return &contract.Error{Code: contract.CodeNotFound, Message: "endpoint not found"}
	}
	switch {
	case errors.Is(err, relay.ErrDeliveryNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "delivery not found"}
	case errors.Is(err, relay.ErrEventNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "event not found"}
	case errors.Is(err, relay.ErrEventTypeNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "event type not found"}
	case errors.Is(err, relay.ErrDLQNotFound):
		return &contract.Error{Code: contract.CodeNotFound, Message: "dead letter entry not found"}
	case errors.Is(err, relay.ErrAlreadyReplayed):
		// The row is kept and marked, so a second replay is refused rather
		// than sent twice. The page reads this code, not the message.
		return &contract.Error{Code: contract.CodeConflict, Message: "this entry has already been replayed"}
	case errors.Is(err, relay.ErrInvalidCursor):
		return &contract.Error{Code: contract.CodeBadRequest, Message: "invalid cursor"}
	case errors.Is(err, relay.ErrInvalidFilter), errors.Is(err, relay.ErrPayloadValidationFailed),
		errors.Is(err, relay.ErrEventTypeDeprecated):
		return &contract.Error{Code: contract.CodeBadRequest, Message: err.Error()}
	case errors.Is(err, redisstore.ErrDeliveryIndexNotBuilt):
		return &contract.Error{Code: contract.CodeUnavailable, Message: err.Error()}
	}
	return &contract.Error{Code: contract.CodeInternal, Message: err.Error()}
}

// fieldLabel names a validation field the way the create form labels it.
// An unknown field falls through as-is: a raw name is still better than none.
func fieldLabel(field string) string {
	switch field {
	case "tenant_id":
		return "Tenant ID"
	case "url":
		return "URL"
	case "event_types":
		return "Event types"
	default:
		return field
	}
}
