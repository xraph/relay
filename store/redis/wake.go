package redis

import (
	"context"
	"fmt"

	relaystore "github.com/xraph/relay/store"
)

// wakeChannel is the pub/sub channel used to signal that new deliveries
// were enqueued. Enqueue paths publish to it; StartWakeListener subscribes.
const wakeChannel = "relay:deliveries:wake"

var _ relaystore.WakeNotifier = (*Store)(nil)

// notifyWake signals listening instances that pending deliveries exist.
// Best-effort by design: polling remains the correctness mechanism, so a
// failed publish only costs poll latency and is not worth failing the
// enqueue over.
func (s *Store) notifyWake(ctx context.Context) {
	_ = s.rdb.Publish(ctx, wakeChannel, "").Err() //nolint:errcheck // best-effort: polling covers missed wakes
}

// StartWakeListener subscribes to the relay wake channel and invokes wake
// for each message. go-redis re-subscribes automatically after connection
// loss, so no manual rebuild loop is needed; messages published while the
// connection was down are simply lost, which polling covers. The returned
// stop function terminates the subscriber and blocks until it has exited.
func (s *Store) StartWakeListener(ctx context.Context, wake func()) (func(), error) {
	ctx, cancel := context.WithCancel(ctx)

	sub := s.rdb.Subscribe(ctx, wakeChannel)
	// Confirm the subscription is established so callers know push is
	// live before relying on it.
	if _, err := sub.Receive(ctx); err != nil {
		cancel()
		_ = sub.Close()
		return nil, fmt.Errorf("relay/redis: start wake listener: %w", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ch := sub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				wake()
			}
		}
	}()

	stop := func() {
		cancel()
		_ = sub.Close()
		<-done
	}
	return stop, nil
}
