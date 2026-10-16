package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

// redactedHost addresses the request the tracing layer sees, so url.full
// reads https://REDACTED and server.address reads REDACTED.
const redactedHost = "REDACTED"

// newTransport wraps base (nil means http.DefaultTransport) so every delivery
// gets an HTTP client span and carries the trace context to the receiver,
// without the endpoint's URL or host reaching that span.
//
// The tracing layer is otelhttp, set up exactly as forge.InstrumentTransport
// sets it up (the global tracer provider and propagator, captured here),
// without importing forge into relay's core packages. Left to itself it would
// copy the full URL onto every client span as url.full and the host as
// server.address. A webhook URL is customer data and is often a credential in
// its own right: Slack and Discord hooks carry their secret in the path,
// others in the query string. So the tracing layer only ever sees a request
// addressed to https://REDACTED, and the real URL goes back on underneath it,
// just before base sends the request.
//
// Errors get the same treatment. A dial error names the host and its address,
// and otelhttp writes err.Error() into the span status, so an error passing up
// through the tracing layer carries only its class, and the full error is put
// back above it. The delivery records the full message for its tenant, as it
// always has.
func newTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	traced := otelhttp.NewTransport(&unmaskTransport{base: base},
		otelhttp.WithTracerProvider(otel.GetTracerProvider()),
		otelhttp.WithPropagators(otel.GetTextMapPropagator()),
	)
	return &maskTransport{traced: traced}
}

// targetKey carries the real request past the tracing layer.
type targetKey struct{}

type target struct {
	url  *url.URL
	host string
}

// maskTransport hands the tracing layer a copy of the request addressed to
// redactedHost, and turns masked errors back into the originals.
type maskTransport struct {
	traced http.RoundTripper
}

func (t *maskTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	ctx := context.WithValue(r.Context(), targetKey{}, target{url: r.URL, host: r.Host})
	masked := r.WithContext(ctx)
	masked.URL = &url.URL{Scheme: r.URL.Scheme, Host: redactedHost}
	masked.Host = ""

	resp, err := t.traced.RoundTrip(masked)
	if err != nil {
		return resp, unmask(err)
	}
	if resp.Body != nil {
		resp.Body = unmaskBody{resp.Body}
	}
	return resp, nil
}

// unmaskTransport sits under the tracing layer. It puts the real URL back on
// the request before base sends it, and masks what base returns.
type unmaskTransport struct {
	base http.RoundTripper
}

func (t *unmaskTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tgt, ok := r.Context().Value(targetKey{}).(target)
	if !ok {
		// Not sent through maskTransport. Nothing to restore.
		return t.base.RoundTrip(r)
	}
	out := r.WithContext(r.Context())
	out.URL = tgt.url
	out.Host = tgt.host

	resp, err := t.base.RoundTrip(out)
	if err != nil {
		return resp, &maskedError{err: err}
	}
	if resp.Body != nil {
		resp.Body = maskBody{resp.Body}
	}
	return resp, nil
}

// maskedError is what the tracing layer sees in place of a transport error:
// its message and its error.type are the error's class alone.
type maskedError struct {
	err error
}

func (e *maskedError) Error() string { return classify(e.err) }

// ErrorType is read by otelhttp for the client span's error.type attribute.
func (e *maskedError) ErrorType() string { return classify(e.err) }

// unmask swaps a maskedError back for the error it hides.
func unmask(err error) error {
	var me *maskedError
	if errors.As(err, &me) {
		return me.err
	}
	return err
}

// maskBody masks the response body's read errors on their way up through the
// tracing layer. io.EOF passes untouched: the tracing layer ends the client
// span when it sees it.
type maskBody struct {
	io.ReadCloser
}

func (b maskBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &maskedError{err: err}
	}
	return n, err
}

// unmaskBody restores what maskBody hid, above the tracing layer.
type unmaskBody struct {
	io.ReadCloser
}

func (b unmaskBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	return n, unmask(err)
}
