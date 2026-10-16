package delivery

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"testing"
)

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	wrap := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://alice@example.com/hook", Err: err}
	}
	cases := []struct {
		err  error
		want string
	}{
		{wrap(context.Canceled), errClassCanceled},
		{wrap(context.DeadlineExceeded), errClassTimeout},
		{wrap(&net.OpError{Op: "dial", Err: timeoutError{}}), errClassTimeout},
		{wrap(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "example.com"}}), errClassDNS},
		{wrap(fmt.Errorf("tls: %w", x509.UnknownAuthorityError{})), errClassTLS},
		{wrap(&net.OpError{Op: "dial", Err: errors.New("connection refused")}), errClassConnection},
		{wrap(io.ErrUnexpectedEOF), errClassConnection},
		{wrap(errors.New("something else")), errClassOther},
	}
	for _, c := range cases {
		if got := classify(c.err); got != c.want {
			t.Errorf("classify(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	if got := classify(nil); got != "" {
		t.Errorf("classify(nil) = %q", got)
	}
}
