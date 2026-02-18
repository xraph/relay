package api

import (
	"net/http"
)

type statsResponse struct {
	PendingDeliveries int64 `json:"pending_deliveries"`
	DLQSize           int64 `json:"dlq_size"`
}

func (h *Handler) getStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	pending, err := h.store.CountPending(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	dlqCount, err := h.store.CountDLQ(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, statsResponse{
		PendingDeliveries: pending,
		DLQSize:           dlqCount,
	})
}
