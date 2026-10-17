package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
)

// Error classes for a delivery that got no response. They are the values of
// the error_type metric label and the description on a failed span, so the
// set stays small and fixed.
const (
	errClassTimeout    = "timeout"
	errClassCanceled   = "canceled"
	errClassDNS        = "dns"
	errClassTLS        = "tls"
	errClassConnection = "connection"
	errClassOther      = "other"
)

// classify reduces an error from sending a delivery to one of the errClass
// values. Order matters: a DNS lookup that timed out is a timeout.
func classify(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return errClassCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errClassTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return errClassTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errClassDNS
	}
	if isTLSError(err) {
		return errClassTLS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return errClassConnection
	}
	return errClassOther
}

func isTLSError(err error) bool {
	var (
		verifyErr   *tls.CertificateVerificationError
		recordErr   tls.RecordHeaderError
		alertErr    tls.AlertError
		unknownAuth x509.UnknownAuthorityError
		hostnameErr x509.HostnameError
		certInvalid x509.CertificateInvalidError
	)
	return errors.As(err, &verifyErr) || errors.As(err, &recordErr) || errors.As(err, &alertErr) ||
		errors.As(err, &unknownAuth) || errors.As(err, &hostnameErr) || errors.As(err, &certInvalid)
}
