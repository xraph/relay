package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/xraph/relay"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/id"
)

func (h *Handler) listDLQ(w http.ResponseWriter, r *http.Request) {
	opts := dlq.ListOpts{
		Offset:   queryInt(r, "offset", 0),
		Limit:    queryInt(r, "limit", 50),
		TenantID: queryParam(r, "tenant_id"),
	}

	entries, err := h.dlqSvc.List(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) replayDLQ(w http.ResponseWriter, r *http.Request) {
	dlqID, err := id.ParseDLQID(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid DLQ ID")
		return
	}

	if replayErr := h.dlqSvc.Replay(r.Context(), dlqID); replayErr != nil {
		if errors.Is(replayErr, relay.ErrDLQNotFound) {
			writeError(w, http.StatusNotFound, "DLQ entry not found")
			return
		}
		writeError(w, http.StatusInternalServerError, replayErr.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type replayBulkRequest struct {
	From string `json:"from"` // RFC3339
	To   string `json:"to"`   // RFC3339
}

func (h *Handler) replayBulkDLQ(w http.ResponseWriter, r *http.Request) {
	var req replayBulkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'from' time format (use RFC3339)")
		return
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid 'to' time format (use RFC3339)")
		return
	}

	count, replayErr := h.dlqSvc.ReplayBulk(r.Context(), from, to)
	if replayErr != nil {
		writeError(w, http.StatusInternalServerError, replayErr.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]int64{"replayed": count})
}
