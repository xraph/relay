package relay

import (
	"errors"
	"net/http"
	"time"

	log "github.com/xraph/go-utils/log"

	"github.com/xraph/relay/catalog"
	"github.com/xraph/relay/delivery"
	"github.com/xraph/relay/dlq"
	"github.com/xraph/relay/endpoint"
	"github.com/xraph/relay/observability"
	"github.com/xraph/relay/store"
)

// Relay is the root webhook delivery engine.
type Relay struct {
	config      Config
	store       store.Store
	catalog     *catalog.Catalog
	validator   *catalog.Validator
	endpointSvc *endpoint.Service
	engine      *delivery.Engine
	dlqSvc      *dlq.Service
	logger      log.Logger
	metrics     *observability.Metrics

	// wakeStop terminates the store wake listener (store.WakeNotifier);
	// nil when the store has no push capability.
	wakeStop func()
}

// Option configures a Relay instance.
type Option func(*Relay) error

// New creates a new Relay with the given options.
func New(opts ...Option) (*Relay, error) {
	r := &Relay{
		config: DefaultConfig(),
		logger: log.NewNoopLogger(),
	}
	for _, opt := range opts {
		if err := opt(r); err != nil {
			return nil, err
		}
	}
	if r.store == nil {
		return nil, ErrNoStore
	}
	r.wireServices()
	return r, nil
}

// WithStore sets the persistence backend for the Relay instance.
func WithStore(s store.Store) Option {
	return func(r *Relay) error {
		r.store = s
		return nil
	}
}

// WithLogger sets the structured logger for the Relay instance.
func WithLogger(logger log.Logger) Option {
	return func(r *Relay) error {
		r.logger = logger
		return nil
	}
}

// WithConcurrency sets the number of delivery worker goroutines.
func WithConcurrency(n int) Option {
	return func(r *Relay) error {
		r.config.Concurrency = n
		return nil
	}
}

// WithPollInterval sets how often the delivery engine checks for pending deliveries.
func WithPollInterval(d time.Duration) Option {
	return func(r *Relay) error {
		r.config.PollInterval = d
		return nil
	}
}

// WithMaxPollInterval caps the delivery engine's idle backoff. Empty polls
// double the poll interval up to this value; new work resets it.
func WithMaxPollInterval(d time.Duration) Option {
	return func(r *Relay) error {
		r.config.MaxPollInterval = d
		return nil
	}
}

// WithBatchSize sets the maximum number of deliveries dequeued per poll cycle.
func WithBatchSize(n int) Option {
	return func(r *Relay) error {
		r.config.BatchSize = n
		return nil
	}
}

// WithHTTPClient sets the client deliveries are sent with, for example one
// whose dialer refuses private networks. A client with no Timeout uses the
// configured request timeout. Relay copies the client and wraps its transport
// in tracing that keeps the endpoint's URL off the spans, so pass a transport
// that isn't traced already (not forge.HTTPClient's).
func WithHTTPClient(c *http.Client) Option {
	return func(r *Relay) error {
		if c == nil {
			return errors.New("relay: HTTP client is required")
		}

		r.config.HTTPClient = c

		return nil
	}
}

// WithRequestTimeout sets the HTTP timeout per delivery attempt.
func WithRequestTimeout(d time.Duration) Option {
	return func(r *Relay) error {
		r.config.RequestTimeout = d
		return nil
	}
}

// WithMaxRetries sets the global default for maximum delivery attempts.
func WithMaxRetries(n int) Option {
	return func(r *Relay) error {
		r.config.MaxRetries = n
		return nil
	}
}

// WithRetrySchedule sets the backoff intervals between retry attempts.
func WithRetrySchedule(schedule []time.Duration) Option {
	return func(r *Relay) error {
		r.config.RetrySchedule = schedule
		return nil
	}
}

// WithShutdownTimeout sets the maximum time to wait for in-flight deliveries on shutdown.
func WithShutdownTimeout(d time.Duration) Option {
	return func(r *Relay) error {
		r.config.ShutdownTimeout = d
		return nil
	}
}

// WithCacheTTL sets the TTL for the catalog's in-memory event type cache.
func WithCacheTTL(d time.Duration) Option {
	return func(r *Relay) error {
		r.config.CacheTTL = d
		return nil
	}
}

// WithMetrics records relay's metrics on m. Build m with
// observability.NewMetrics over a go-utils metric factory; in a Forge app the
// extension passes app.Metrics() for you. Without it relay records no
// metrics.
func WithMetrics(m *observability.Metrics) Option {
	return func(r *Relay) error {
		r.metrics = m
		return nil
	}
}

// WithTracer does nothing. Relay traces through the global tracer provider,
// which forge.New installs, so there is no tracer to pass.
//
// Deprecated: Relay traces without a Tracer. Drop the option.
func WithTracer(*observability.Tracer) Option {
	return func(*Relay) error {
		return nil
	}
}
