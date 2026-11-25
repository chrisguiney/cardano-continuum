// Package sched orders simulated activity. It holds the event queue, arrival
// processes (modulated Poisson and Hawkes), slot and epoch arithmetic, and
// the clocks that pace a run. Everything except Wall is pure: it reads no
// wall clock and starts no goroutine, so a run is a function of its seed.
package sched

// Time is simulated time in milliseconds since the network's system start.
type Time int64

// Event is an opaque payload carried by the Queue.
type Event any

// Handle identifies a pushed event so it can be cancelled. Handles are
// never reused within one Queue.
type Handle uint64

type entry struct {
	at  Time
	seq Handle
	ev  Event
}

// Queue is a min-heap of events ordered by (time, push sequence). The
// sequence number makes the order total: events at the same time pop in the
// order they were pushed, independent of heap layout or map iteration. The
// zero value is an empty queue ready to use.
type Queue struct {
	heap []entry
	pos  map[Handle]int // heap index of each pending event
	next Handle
}

func less(a, b entry) bool {
	if a.at != b.at {
		return a.at < b.at
	}
	return a.seq < b.seq
}

// Len reports the number of pending events.
func (q *Queue) Len() int { return len(q.heap) }

// Push schedules ev at time at and returns a handle for Cancel.
func (q *Queue) Push(at Time, ev Event) Handle {
	if q.pos == nil {
		q.pos = make(map[Handle]int)
	}
	h := q.next
	q.next++
	q.heap = append(q.heap, entry{at: at, seq: h, ev: ev})
	i := len(q.heap) - 1
	q.pos[h] = i
	q.up(i)
	return h
}

// Cancel removes a pending event. A handle that has already popped or been
// cancelled is ignored.
func (q *Queue) Cancel(h Handle) {
	i, ok := q.pos[h]
	if !ok {
		return
	}
	q.removeAt(i)
}

// Peek returns the time of the next event without removing it.
func (q *Queue) Peek() (Time, bool) {
	if len(q.heap) == 0 {
		return 0, false
	}
	return q.heap[0].at, true
}

// Pop removes and returns the earliest event.
func (q *Queue) Pop() (Time, Event, bool) {
	if len(q.heap) == 0 {
		return 0, nil, false
	}
	e := q.heap[0]
	q.removeAt(0)
	return e.at, e.ev, true
}

func (q *Queue) removeAt(i int) {
	last := len(q.heap) - 1
	delete(q.pos, q.heap[i].seq)
	if i != last {
		q.heap[i] = q.heap[last]
		q.pos[q.heap[i].seq] = i
	}
	q.heap[last] = entry{} // drop the payload reference
	q.heap = q.heap[:last]
	if i < last {
		q.down(i)
		q.up(i)
	}
}

func (q *Queue) swap(i, j int) {
	q.heap[i], q.heap[j] = q.heap[j], q.heap[i]
	q.pos[q.heap[i].seq] = i
	q.pos[q.heap[j].seq] = j
}

func (q *Queue) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !less(q.heap[i], q.heap[p]) {
			return
		}
		q.swap(i, p)
		i = p
	}
}

func (q *Queue) down(i int) {
	n := len(q.heap)
	for {
		l := 2*i + 1
		if l >= n {
			return
		}
		m := l
		if r := l + 1; r < n && less(q.heap[r], q.heap[l]) {
			m = r
		}
		if !less(q.heap[m], q.heap[i]) {
			return
		}
		q.swap(i, m)
		i = m
	}
}
