package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/xraph/relay"
	"github.com/xraph/relay/event"
	"github.com/xraph/relay/id"
)

type createEventRequest struct {
	Type           string          `json:"type"`
	TenantID       string          `json:"tenant_id"`
	Data           json.RawMessage `json:"data"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

func (h *Handler) createEvent(w http.ResponseWriter, r *http.Request) {
	var req createEventRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}
	if req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}

	evt := &event.Event{
		ID:             id.NewEventID(),
		Type:           req.Type,
		TenantID:       req.TenantID,
		Data:           req.Data,
		IdempotencyKey: req.IdempotencyKey,
	}

	if err := h.store.CreateEvent(r.Context(), evt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, evt)
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	opts := event.ListOpts{
		Offset: queryInt(r, "offset", 0),
		Limit:  queryInt(r, "limit", 50),
		Type:   queryParam(r, "type"),
	}

	events, err := h.store.ListEvents(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, events)
}

func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) {
	evtID, err := id.ParseEventID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid event ID")
		return
	}

	evt, getErr := h.store.GetEvent(r.Context(), evtID)
	if getErr != nil {
		if errors.Is(getErr, relay.ErrEventNotFound) {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		writeError(w, http.StatusInternalServerError, getErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, evt)
}
