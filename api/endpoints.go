package api

import (
	"errors"
	"net/http"

	"github.com/xraph/relay"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/id"
)

type createEndpointRequest struct {
	TenantID   string            `json:"tenant_id"`
	URL        string            `json:"url"`
	EventTypes []string          `json:"event_types"`
	Headers    map[string]string `json:"headers,omitempty"`
	RateLimit  int               `json:"rate_limit,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type updateEndpointRequest struct {
	URL        string            `json:"url"`
	EventTypes []string          `json:"event_types"`
	Headers    map[string]string `json:"headers,omitempty"`
	RateLimit  int               `json:"rate_limit,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

func (h *Handler) createEndpoint(w http.ResponseWriter, r *http.Request) {
	var req createEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	input := endpoint.Input{
		TenantID:   req.TenantID,
		URL:        req.URL,
		EventTypes: req.EventTypes,
		Headers:    req.Headers,
		RateLimit:  req.RateLimit,
		Metadata:   req.Metadata,
	}

	ep, err := h.endpointSvc.Create(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, ep)
}

func (h *Handler) listEndpoints(w http.ResponseWriter, r *http.Request) {
	tenantID := queryParam(r, "tenant_id")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id query parameter is required")
		return
	}

	opts := endpoint.ListOpts{
		Offset: queryInt(r, "offset", 0),
		Limit:  queryInt(r, "limit", 50),
	}

	eps, err := h.endpointSvc.List(r.Context(), tenantID, opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, eps)
}

func (h *Handler) getEndpoint(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	ep, getErr := h.endpointSvc.Get(r.Context(), epID)
	if getErr != nil {
		if errors.Is(getErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, getErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, ep)
}

func (h *Handler) updateEndpoint(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	var req updateEndpointRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	input := endpoint.Input{
		URL:        req.URL,
		EventTypes: req.EventTypes,
		Headers:    req.Headers,
		RateLimit:  req.RateLimit,
		Metadata:   req.Metadata,
	}

	ep, updateErr := h.endpointSvc.Update(r.Context(), epID, input)
	if updateErr != nil {
		if errors.Is(updateErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, updateErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, ep)
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	if deleteErr := h.endpointSvc.Delete(r.Context(), epID); deleteErr != nil {
		if errors.Is(deleteErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, deleteErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) enableEndpoint(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	if setErr := h.endpointSvc.SetEnabled(r.Context(), epID, true); setErr != nil {
		if errors.Is(setErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, setErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) disableEndpoint(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	if setErr := h.endpointSvc.SetEnabled(r.Context(), epID, false); setErr != nil {
		if errors.Is(setErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, setErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	newSecret, rotateErr := h.endpointSvc.RotateSecret(r.Context(), epID)
	if rotateErr != nil {
		if errors.Is(rotateErr, relay.ErrEndpointNotFound) {
			writeError(w, http.StatusNotFound, "endpoint not found")
			return
		}
		writeError(w, http.StatusInternalServerError, rotateErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"secret": newSecret})
}
