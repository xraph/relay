// Package api provides the Admin HTTP API for Relay webhook management.
//
// All routes are mounted under a configurable prefix (default: /webhooks).
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/store"
)

// Handler is the root HTTP handler for the Relay admin API.
type Handler struct {
	store       store.Store
	catalog     *catalog.Catalog
	endpointSvc *endpoint.Service
	dlqSvc      *dlq.Service
	logger      *slog.Logger
	mux         *http.ServeMux
}

// NewHandler creates a new admin API handler.
func NewHandler(
	s store.Store,
	cat *catalog.Catalog,
	epSvc *endpoint.Service,
	dlqSvc *dlq.Service,
	logger *slog.Logger,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}

	h := &Handler{
		store:       s,
		catalog:     cat,
		endpointSvc: epSvc,
		dlqSvc:      dlqSvc,
		logger:      logger,
		mux:         http.NewServeMux(),
	}

	h.registerRoutes()
	return h
}

func (h *Handler) registerRoutes() {
	// Event types
	h.mux.HandleFunc("POST /event-types", h.createEventType)
	h.mux.HandleFunc("GET /event-types", h.listEventTypes)
	h.mux.HandleFunc("GET /event-types/{name}", h.getEventType)
	h.mux.HandleFunc("DELETE /event-types/{name}", h.deleteEventType)

	// Endpoints
	h.mux.HandleFunc("POST /endpoints", h.createEndpoint)
	h.mux.HandleFunc("GET /endpoints", h.listEndpoints)
	h.mux.HandleFunc("GET /endpoints/{id}", h.getEndpoint)
	h.mux.HandleFunc("PUT /endpoints/{id}", h.updateEndpoint)
	h.mux.HandleFunc("DELETE /endpoints/{id}", h.deleteEndpoint)
	h.mux.HandleFunc("PATCH /endpoints/{id}/enable", h.enableEndpoint)
	h.mux.HandleFunc("PATCH /endpoints/{id}/disable", h.disableEndpoint)
	h.mux.HandleFunc("POST /endpoints/{id}/rotate-secret", h.rotateSecret)

	// Events
	h.mux.HandleFunc("POST /events", h.createEvent)
	h.mux.HandleFunc("GET /events", h.listEvents)
	h.mux.HandleFunc("GET /events/{id}", h.getEvent)

	// Deliveries
	h.mux.HandleFunc("GET /endpoints/{id}/deliveries", h.listDeliveries)

	// DLQ
	h.mux.HandleFunc("GET /dlq", h.listDLQ)
	h.mux.HandleFunc("POST /dlq/{id}/replay", h.replayDLQ)
	h.mux.HandleFunc("POST /dlq/replay", h.replayBulkDLQ)

	// Stats
	h.mux.HandleFunc("GET /stats", h.getStats)
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.withMiddleware(h.mux).ServeHTTP(w, r)
}

func (h *Handler) withMiddleware(next http.Handler) http.Handler {
	return h.panicRecovery(h.logging(next))
}

func (h *Handler) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		h.logger.Info("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (h *Handler) panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				h.logger.Error("panic recovered",
					"error", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// JSON helpers.

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // best effort
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// queryParam returns a query parameter value, or empty string if not present.
func queryParam(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

// queryInt returns a query parameter as int or a default value.
func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	var n int
	for _, c := range v {
		if c < '0' || c > '9' {
			return defaultVal
		}
		n = n*10 + int(c-'0')
	}
	return n
}
