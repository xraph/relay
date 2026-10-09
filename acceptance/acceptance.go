// Package acceptance defines Relay's reliable acceptance protocol.
package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xraph/relay/id"
)

const (
	Version            = 1
	MaxDataBytes       = 1 << 20
	MaxIdentifierBytes = 256
	MaxRecipients      = 1000
)

var (
	ErrInvalid           = errors.New("relay: invalid reliable acceptance request")
	ErrConflict          = errors.New("relay: acceptance content conflict")
	ErrUnsupported       = errors.New("relay: reliable acceptance unsupported")
	ErrTooManyRecipients = errors.New("relay: acceptance recipient limit exceeded")
)

// Request carries trusted identity and ownership supplied by the host. Hosts must
// authorize these fields; possession of a source key does not establish access.
// OrgID may be empty for an app-owned tenant. All other identity fields are required.
type Request struct {
	Producer          string          `json:"producer"`
	InstallationID    string          `json:"installation_id"`
	SourceKey         string          `json:"source_key"`
	SourceFingerprint string          `json:"source_fingerprint"`
	AppID             string          `json:"app_id"`
	OrgID             string          `json:"org_id"`
	TenantID          string          `json:"tenant_id"`
	Type              string          `json:"type"`
	Data              json.RawMessage `json:"data"`
}

type Recipient struct {
	EndpointID id.ID `json:"endpoint_id"`
	DeliveryID id.ID `json:"delivery_id"`
}

// Receipt survives event retention. Recipients pin membership and delivery IDs,
// while endpoint configuration is loaded again at delivery time.
type Receipt struct {
	Version           int         `json:"version"`
	Producer          string      `json:"producer"`
	InstallationID    string      `json:"installation_id"`
	SourceKey         string      `json:"source_key"`
	SourceFingerprint string      `json:"source_fingerprint"`
	AppID             string      `json:"app_id"`
	OrgID             string      `json:"org_id"`
	TenantID          string      `json:"tenant_id"`
	Fingerprint       string      `json:"fingerprint"`
	EventID           id.ID       `json:"event_id"`
	AcceptedAt        time.Time   `json:"accepted_at"`
	Recipients        []Recipient `json:"recipients"`
}

// Store is optional. AcceptEvent commits the event, receipt and complete fanout
// atomically, resolving a matching receipt before any mutable validation.
type Store interface {
	AcceptEvent(context.Context, Request, int) (*Receipt, error)
}

func Identifier(v string) bool {
	return v != "" && len(v) <= MaxIdentifierBytes && utf8.ValidString(v) && strings.TrimSpace(v) == v && !strings.ContainsFunc(v, unicode.IsControl)
}

// Normalize validates and detaches caller data. Fingerprint uses version 1's
// canonical JSON rules; it never trusts a caller-supplied semantic hash.
func (r Request) Normalize() (Request, error) {
	for _, v := range []string{r.Producer, r.InstallationID, r.SourceKey, r.AppID, r.TenantID, r.Type} {
		if !Identifier(v) {
			return Request{}, ErrInvalid
		}
	}
	if r.OrgID != "" && !Identifier(r.OrgID) {
		return Request{}, ErrInvalid
	}
	if len(r.SourceFingerprint) != sha256.Size*2 {
		return Request{}, ErrInvalid
	}
	b, err := hex.DecodeString(r.SourceFingerprint)
	if err != nil || len(b) != sha256.Size || strings.ToLower(r.SourceFingerprint) != r.SourceFingerprint {
		return Request{}, ErrInvalid
	}
	r.Data, err = CanonicalJSON(r.Data)
	if err != nil {
		return Request{}, err
	}
	return r, nil
}

func Fingerprint(r Request) (string, error) {
	r, err := r.Normalize()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Version int `json:"version"`
		Request
	}{Version: Version, Request: r})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Identity is unambiguous even when identifiers contain punctuation.
func (r Request) Identity() string {
	return strconv.Itoa(len(r.Producer)) + ":" + r.Producer + strconv.Itoa(len(r.InstallationID)) + ":" + r.InstallationID + strconv.Itoa(len(r.SourceKey)) + ":" + r.SourceKey
}

func (r *Receipt) Verify(req Request) error {
	fp, err := Fingerprint(req)
	if err != nil {
		return err
	}
	if r.Version != Version || r.Producer != req.Producer || r.InstallationID != req.InstallationID || r.SourceKey != req.SourceKey || r.SourceFingerprint != req.SourceFingerprint || r.AppID != req.AppID || r.OrgID != req.OrgID || r.TenantID != req.TenantID || r.Fingerprint != fp {
		return ErrConflict
	}
	return nil
}

func (r *Receipt) Clone() *Receipt {
	cp := *r
	cp.Recipients = append([]Recipient{}, r.Recipients...)
	return &cp
}
