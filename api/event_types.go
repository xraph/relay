package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/xraph/relay"
	"github.com/xraph/relay/catalog"
)

type createEventTypeRequest struct {
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Group         string            `json:"group,omitempty"`
	Schema        json.RawMessage   `json:"schema,omitempty"`
	SchemaVersion string            `json:"schema_version,omitempty"`
	Version       string            `json:"version,omitempty"`
	ScopeAppID    string            `json:"scope_app_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) createEventType(w http.ResponseWriter, r *http.Request) {
	var req createEventTypeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	def := catalog.WebhookDefinition{
		Name:          req.Name,
		Description:   req.Description,
		Group:         req.Group,
		Schema:        req.Schema,
		SchemaVersion: req.SchemaVersion,
		Version:       req.Version,
	}

	var opts []catalog.RegisterOption
	if req.ScopeAppID != "" {
		opts = append(opts, catalog.WithScopeAppID(req.ScopeAppID))
	}
	if req.Metadata != nil {
		opts = append(opts, catalog.WithMetadata(req.Metadata))
	}

	et, err := h.catalog.RegisterType(r.Context(), def, opts...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, et)
}

func (h *Handler) listEventTypes(w http.ResponseWriter, r *http.Request) {
	opts := catalog.ListOpts{
		Offset:            queryInt(r, "offset", 0),
		Limit:             queryInt(r, "limit", 50),
		Group:             queryParam(r, "group"),
		IncludeDeprecated: queryParam(r, "include_deprecated") == "true",
	}

	types, err := h.catalog.ListTypes(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, types)
}

func (h *Handler) getEventType(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	et, err := h.catalog.GetType(r.Context(), name)
	if err != nil {
		if errors.Is(err, relay.ErrEventTypeNotFound) {
			writeError(w, http.StatusNotFound, "event type not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, et)
}

func (h *Handler) deleteEventType(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	err := h.catalog.DeleteType(r.Context(), name)
	if err != nil {
		if errors.Is(err, relay.ErrEventTypeNotFound) {
			writeError(w, http.StatusNotFound, "event type not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
