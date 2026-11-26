package httpapi

import "sync"

// Hub is the decision feed: a ring buffer of recent decisions and the
// subscribers that want the next ones. Publish never blocks: a subscriber
// whose buffer is full is disconnected, so one stalled browser tab cannot
// slow the engine.
type Hub struct {
	maxClients int
	buf        int

	mu      sync.Mutex // protects ring, evicted, subs, next, dropped, closed
	ring    []Decision
	head    int // index of the oldest entry once the ring is full
	cap     int
	evicted uint64 // highest ID the ring has dropped; 0 if none
	subs    map[int]*sub
	next    int
	dropped int
	closed  bool
}

type sub struct {
	f Filter
	c chan Decision
}

// NewHub returns a hub that keeps ringSize decisions, serves at most
// maxClients subscribers and buffers buf undelivered decisions for each.
func NewHub(ringSize, maxClients, buf int) *Hub {
	return &Hub{cap: max(ringSize, 1), maxClients: maxClients, buf: max(buf, 1), subs: map[int]*sub{}}
}

// Publish adds d to the ring and delivers it to matching subscribers.
func (h *Hub) Publish(d Decision) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if len(h.ring) < h.cap {
		h.ring = append(h.ring, d)
	} else {
		h.evicted = h.ring[h.head].ID
		h.ring[h.head] = d
		h.head = (h.head + 1) % h.cap
	}
	for id, s := range h.subs {
		if !s.f.Match(d) {
			continue
		}
		select {
		case s.c <- d:
		default:
			delete(h.subs, id)
			close(s.c)
			h.dropped++
		}
	}
}

// Subscribe registers a client. With after set, Replay holds the ring's
// matching decisions with ID above *after, and Gap is true when the ring has
// dropped decisions the client has not seen. With after nil the client gets
// live decisions only. Replay and registration happen under one lock, so no
// decision is missed or repeated between them.
func (h *Hub) Subscribe(f Filter, after *uint64) (*Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosed
	}
	if len(h.subs) >= h.maxClients {
		return nil, ErrTooManyClients
	}
	s := &sub{f: f, c: make(chan Decision, h.buf)}
	id := h.next
	h.next++
	h.subs[id] = s
	out := &Subscription{C: s.c}
	if after != nil {
		out.Gap = *after < h.evicted
		for i := range h.ring {
			d := h.ring[(h.head+i)%len(h.ring)]
			if d.ID > *after && f.Match(d) {
				out.Replay = append(out.Replay, d)
			}
		}
	}
	var once sync.Once
	out.Close = func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if _, ok := h.subs[id]; ok {
				delete(h.subs, id)
				close(s.c)
			}
		})
	}
	return out, nil
}

// Subscribers is the number of connected clients.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Dropped counts clients disconnected for falling behind.
func (h *Hub) Dropped() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dropped
}

// Close disconnects every client and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for id, s := range h.subs {
		delete(h.subs, id)
		close(s.c)
	}
}
