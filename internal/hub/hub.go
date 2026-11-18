package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/boldfield/syndic/internal/journal"
)

type Emit func(turnID, kind string, payload any) error

type Hub struct {
	store     *journal.Store
	queueSize int
	mu        sync.Mutex
	subs      map[*Subscription]bool
}

func New(store *journal.Store, queueSize int) *Hub {
	if queueSize <= 0 {
		queueSize = 1024
	}
	return &Hub{
		store:     store,
		queueSize: queueSize,
		subs:      make(map[*Subscription]bool),
	}
}

type Subscription struct {
	hub          *Hub
	queue        chan journal.Event
	lagging      bool
	lastSeq      int64
	watermark    int64
	replayBuffer []journal.Event
	replayIdx    int
	mu           sync.Mutex
	closed       bool
}

var (
	ErrLagging = errors.New("subscriber lagging")
	ErrClosed  = errors.New("subscription closed")
)

type LaggingError struct {
	LastSeq int64
}

func (e *LaggingError) Error() string {
	return ErrLagging.Error()
}

func (e *LaggingError) Is(err error) bool {
	return err == ErrLagging
}

func (h *Hub) Tx(fn func(tx *journal.Tx, emit Emit) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	var emittedEvents []journal.Event

	err := h.store.Tx(func(tx *journal.Tx) error {
		emitFn := func(turnID, kind string, payload any) error {
			data, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			event, err := tx.Emit(turnID, kind, json.RawMessage(data))
			if err != nil {
				return err
			}
			emittedEvents = append(emittedEvents, event)
			return nil
		}

		return fn(tx, emitFn)
	})

	if err != nil {
		return err
	}

	var laggingSubs []*Subscription
	for sub := range h.subs {
		skipThisSub := false
		for _, event := range emittedEvents {
			if skipThisSub {
				break
			}
			select {
			case sub.queue <- event:
			default:
				sub.mu.Lock()
				sub.lagging = true
				sub.mu.Unlock()
				laggingSubs = append(laggingSubs, sub)
				skipThisSub = true
			}
		}
	}

	for _, sub := range laggingSubs {
		delete(h.subs, sub)
	}

	return nil
}

func (h *Hub) Subscribe(sinceSeq int64) (*Subscription, int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	watermark, err := h.store.MaxSeq()
	if err != nil {
		return nil, 0, err
	}

	sub := &Subscription{
		hub:       h,
		queue:     make(chan journal.Event, h.queueSize),
		watermark: watermark,
		lastSeq:   sinceSeq,
	}

	h.subs[sub] = true

	return sub, watermark + 1, nil
}

func (s *Subscription) Next(ctx context.Context) (journal.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return journal.Event{}, ErrClosed
	}
	lagging := s.lagging
	s.mu.Unlock()

	for {
		if s.replayIdx < len(s.replayBuffer) {
			event := s.replayBuffer[s.replayIdx]
			s.mu.Lock()
			s.lastSeq = event.Seq
			s.mu.Unlock()
			s.replayIdx++
			return event, nil
		}

		if s.lastSeq < s.watermark {
			events, err := s.hub.store.EventsSince(s.lastSeq, 1024)
			if err != nil {
				return journal.Event{}, err
			}
			var filtered []journal.Event
			for _, e := range events {
				if e.Seq <= s.watermark {
					filtered = append(filtered, e)
				}
			}
			s.mu.Lock()
			s.replayBuffer = filtered
			s.replayIdx = 0
			s.mu.Unlock()
			if len(filtered) > 0 {
				continue
			}
		}

		if lagging {
			select {
			case event, ok := <-s.queue:
				if !ok {
					return journal.Event{}, ErrClosed
				}
				s.mu.Lock()
				if event.Seq <= s.watermark {
					s.mu.Unlock()
					continue
				}
				s.lastSeq = event.Seq
				s.mu.Unlock()
				return event, nil
			default:
				return journal.Event{}, &LaggingError{LastSeq: s.lastSeq}
			}
		}

		select {
		case event, ok := <-s.queue:
			if !ok {
				return journal.Event{}, ErrClosed
			}
			s.mu.Lock()
			if event.Seq <= s.watermark {
				s.mu.Unlock()
				continue
			}
			s.lastSeq = event.Seq
			s.mu.Unlock()
			return event, nil
		case <-ctx.Done():
			return journal.Event{}, ctx.Err()
		}
	}
}

func (s *Subscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.hub.mu.Lock()
	delete(s.hub.subs, s)
	s.hub.mu.Unlock()

	close(s.queue)
}
