package store

import "sync"

// Waiter is a registration for "tell me when one of these keys might have
// data". The channel is buffered and receives at most one signal, so a slow
// waiter cannot accumulate wakeups.
type Waiter struct {
	keys []string
	C    chan struct{}
}

type waiterSet struct {
	mu    sync.Mutex
	byKey map[string][]*Waiter
}

func newWaiterSet() *waiterSet {
	return &waiterSet{byKey: make(map[string][]*Waiter)}
}

func (s *waiterSet) register(keys []string) *Waiter {
	w := &Waiter{keys: keys, C: make(chan struct{}, 1)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		s.byKey[key] = append(s.byKey[key], w)
	}
	return w
}

func (s *waiterSet) unregister(w *Waiter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range w.keys {
		queue := s.byKey[key]
		for i, item := range queue {
			if item == w {
				queue = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		if len(queue) == 0 {
			delete(s.byKey, key)
		} else {
			s.byKey[key] = queue
		}
	}
}

// signal wakes the oldest waiter that is not already awake. If the oldest has
// a pending wakeup, the next one is signalled so two pushes wake two waiters.
func (s *waiterSet) signal(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range s.byKey[key] {
		select {
		case w.C <- struct{}{}:
			return
		default:
		}
	}
}

func (s *waiterSet) wakeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, queue := range s.byKey {
		for _, w := range queue {
			select {
			case w.C <- struct{}{}:
			default:
			}
		}
	}
}

// Wait registers a waiter for keys.
func (s *Store) Wait(keys []string) *Waiter {
	return s.waiters.register(keys)
}

// CancelWait removes a waiter. It must be called after Wait, whether the wait
// succeeded or timed out.
func (s *Store) CancelWait(w *Waiter) {
	s.waiters.unregister(w)
}

// WakeAll wakes every blocked waiter, so a shutdown does not leave clients
// waiting for a timeout that may be infinite.
func (s *Store) WakeAll() {
	s.waiters.wakeAll()
}
