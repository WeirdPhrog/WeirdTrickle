package magitrickle

import (
	"time"

	"github.com/rs/zerolog/log"
)

type networkRetry struct {
	timer *time.Timer
	ready <-chan time.Time
	delay time.Duration
}

func (r *networkRetry) fail() {
	if r.ready != nil {
		return
	}
	r.delay = min(max(time.Second, r.delay*2), 30*time.Second)
	if r.timer == nil {
		r.timer = time.NewTimer(r.delay)
	} else {
		r.timer.Reset(r.delay)
	}
	r.ready = r.timer.C
}

func (r *networkRetry) fired() { r.ready = nil }
func (r *networkRetry) pause() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.ready = nil
}
func (r *networkRetry) clear() {
	r.pause()
	r.delay = 0
}

type networkSubscription[T any] struct {
	updates     <-chan T
	done        chan struct{}
	retry       networkRetry
	subscribe   func() (chan T, chan struct{}, error)
	name        string
	connectedAt time.Time
}

func (s *networkSubscription[T]) stopActive() {
	if s.done != nil {
		close(s.done)
		s.done = nil
		// netlink may be blocked sending a queued update when its socket closes.
		// Drain the old channel so its goroutine can exit; reconciliation reads
		// current kernel state after the new subscription is established.
		if s.updates != nil {
			go func(ch <-chan T) {
				for range ch {
				}
			}(s.updates)
		}
	}
	s.updates = nil
}

func (s *networkSubscription[T]) connect() bool {
	s.stopActive()
	updates, done, err := s.subscribe()
	if err != nil {
		log.Error().Err(err).Str("subscription", s.name).Msg("netlink subscription failed; retaining routing and retrying")
		s.retry.fail()
		return false
	}
	s.updates, s.done = updates, done
	s.connectedAt = time.Now()
	s.retry.pause()
	return true
}

func (s *networkSubscription[T]) lost() {
	if time.Since(s.connectedAt) > 30*time.Second {
		s.retry.delay = 0
	}
	s.stopActive()
	s.retry.fail()
	log.Warn().Str("subscription", s.name).Msg("netlink subscription closed; retaining routing and reconnecting")
}

func (s *networkSubscription[T]) close() { s.stopActive(); s.retry.clear() }
