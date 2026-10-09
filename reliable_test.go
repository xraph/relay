package relay_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/xraph/relay"
	"github.com/xraph/relay/acceptance"
	"github.com/xraph/relay/store"
)

type lostAckStore struct {
	store.Store
	acceptor acceptance.Store
	receipt  *acceptance.Receipt
}

func (s *lostAckStore) AcceptEvent(ctx context.Context, req acceptance.Request, maxAttempts int) (*acceptance.Receipt, error) {
	r, err := s.acceptor.AcceptEvent(ctx, req, maxAttempts)
	if err != nil {
		return nil, err
	}
	if s.receipt == nil {
		s.receipt = r
		return nil, errors.New("commit acknowledgement lost")
	}
	return r, nil
}
func TestReliableLostAcknowledgement(t *testing.T) {
	r, s := setup(t)
	registerType(t, r, "test.accepted")
	wrapper := &lostAckStore{Store: s, acceptor: s}
	sender, err := relay.New(relay.WithStore(wrapper))
	if err != nil {
		t.Fatal(err)
	}
	req := acceptance.Request{Producer: "producer", InstallationID: "instance", SourceKey: "key", SourceFingerprint: strings.Repeat("a", 64), AppID: "app", TenantID: "tenant", Type: "test.accepted", Data: []byte(`1`)}
	if receipt, sendErr := sender.SendReliable(context.Background(), req); sendErr == nil || receipt != nil {
		t.Fatal("expected uncertain outcome")
	}
	if deleteErr := s.DeleteType(context.Background(), req.Type); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	receipt, err := sender.SendReliable(context.Background(), req)
	if err != nil || !reflect.DeepEqual(receipt, wrapper.receipt) {
		t.Fatal("lost acknowledgement not recovered", err)
	}
	unsupported, err := relay.New(relay.WithStore(struct{ store.Store }{s}))
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := unsupported.SendReliable(context.Background(), req); !errors.Is(err, acceptance.ErrUnsupported) || receipt != nil {
		t.Fatal("unsupported store fallback", err)
	}
}
