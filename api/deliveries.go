package api

import (
	"net/http"

	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/id"
)

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	epID, err := id.ParseEndpointID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid endpoint ID")
		return
	}

	opts := delivery.ListOpts{
		Offset: queryInt(r, "offset", 0),
		Limit:  queryInt(r, "limit", 50),
	}

	stateStr := queryParam(r, "state")
	if stateStr != "" {
		state := delivery.State(stateStr)
		opts.State = &state
	}

	deliveries, listErr := h.store.ListByEndpoint(r.Context(), epID, opts)
	if listErr != nil {
		writeError(w, http.StatusInternalServerError, listErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, deliveries)
}
