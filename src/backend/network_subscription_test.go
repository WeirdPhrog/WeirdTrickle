package magitrickle

import (
	"errors"
	"testing"
	"time"
)

func TestNetworkSubscriptionReconnectsWithoutShuttingDown(t *testing.T) {
	var updates chan int
	var done chan struct{}
	attempts := 0
	s := &networkSubscription[int]{name: "test", subscribe: func() (chan int, chan struct{}, error) {
		attempts++
		if attempts == 1 {
			return nil, nil, errors.New("receive buffer unavailable")
		}
		updates, done = make(chan int, 1), make(chan struct{})
		return updates, done, nil
	}}
	defer s.close()
	if s.connect() || s.updates != nil || s.retry.ready == nil || s.retry.delay != time.Second {
		t.Fatal("initial failure did not schedule recovery")
	}
	s.retry.fired()
	if !s.connect() || s.retry.ready != nil {
		t.Fatal("reconnection failed")
	}
	oldDone := done
	close(updates)
	s.lost()
	select {
	case <-oldDone:
	default:
		t.Fatal("old subscription was not closed")
	}
	if s.updates != nil || s.retry.ready == nil {
		t.Fatal("closed channel would spin the event loop")
	}
	s.retry.fired()
	if !s.connect() || attempts != 3 {
		t.Fatal("lost subscription did not reconnect")
	}
	updates <- 42
	if <-s.updates != 42 {
		t.Fatal("new events not delivered")
	}
	close(updates)
}

func TestNetworkRetryBackoffAndReset(t *testing.T) {
	var retry networkRetry
	defer retry.clear()
	for _, expected := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		retry.fail()
		if retry.delay != expected*time.Second {
			t.Fatalf("delay=%s", retry.delay)
		}
		ready := retry.ready
		retry.fail()
		if retry.ready != ready || retry.delay != expected*time.Second {
			t.Fatal("new events postponed an already scheduled retry")
		}
		retry.fired()
	}
	retry.clear()
	retry.fail()
	if retry.delay != time.Second {
		t.Fatal("healthy recovery did not reset retry delay")
	}
}
