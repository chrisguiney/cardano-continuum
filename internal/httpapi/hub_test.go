package httpapi

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func recv(t *testing.T, c <-chan Decision) Decision {
	t.Helper()
	select {
	case d, ok := <-c:
		if !ok {
			t.Fatal("channel closed")
		}
		return d
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a decision")
	}
	return Decision{}
}

func ids(ds []Decision) []uint64 {
	var out []uint64
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func eqIDs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFilterMatch(t *testing.T) {
	two := uint64(2)
	d := Decision{ID: 1, Actor: 2, Profile: "hodl", Kind: "Submitted"}
	tests := []struct {
		name string
		f    Filter
		want bool
	}{
		{"empty", Filter{}, true},
		{"actor hit", Filter{Actor: &two}, true},
		{"profile miss", Filter{Profile: "yield"}, false},
		{"kind set hit", Filter{Kinds: []string{"Skipped", "Submitted"}}, true},
		{"kind set miss", Filter{Kinds: []string{"Skipped"}}, false},
		{"all must hold", Filter{Profile: "hodl", Kinds: []string{"Skipped"}}, false},
	}
	for _, tt := range tests {
		if got := tt.f.Match(d); got != tt.want {
			t.Errorf("%s: got %t want %t", tt.name, got, tt.want)
		}
	}
}

func TestHubFilteredDelivery(t *testing.T) {
	h := NewHub(8, 4, 4)
	two := uint64(2)
	sub, err := h.Subscribe(Filter{Actor: &two}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Publish(Decision{ID: 1, Actor: 1})
	h.Publish(Decision{ID: 2, Actor: 2})
	h.Publish(Decision{ID: 3, Actor: 2})
	if a, b := recv(t, sub.C), recv(t, sub.C); a.ID != 2 || b.ID != 3 {
		t.Fatalf("got %d, %d", a.ID, b.ID)
	}
	sub.Close()
	sub.Close() // idempotent
	if n := h.Subscribers(); n != 0 {
		t.Fatalf("%d subscribers after close", n)
	}
}

func TestHubReplayFromLastEventID(t *testing.T) {
	h := NewHub(3, 4, 8)
	for i := uint64(1); i <= 5; i++ {
		h.Publish(Decision{ID: i * 10, Actor: 1})
	}
	// Ring holds 30, 40, 50; 10 and 20 were evicted.
	tests := []struct {
		name   string
		after  *uint64
		replay []uint64
		gap    bool
	}{
		{"none means live only", nil, nil, false},
		{"inside the ring", ptr(30), []uint64{40, 50}, false},
		{"exactly at the eviction edge", ptr(20), []uint64{30, 40, 50}, false},
		{"before the ring", ptr(10), []uint64{30, 40, 50}, true},
		{"zero replays the ring", ptr(0), []uint64{30, 40, 50}, true},
		{"caught up", ptr(50), nil, false},
	}
	for _, tt := range tests {
		sub, err := h.Subscribe(Filter{}, tt.after)
		if err != nil {
			t.Fatal(err)
		}
		if !eqIDs(ids(sub.Replay), tt.replay) || sub.Gap != tt.gap {
			t.Errorf("%s: replay %v gap %t, want %v %t", tt.name, ids(sub.Replay), sub.Gap, tt.replay, tt.gap)
		}
		sub.Close()
	}
}

func ptr(v uint64) *uint64 { return &v }

func TestHubReplayThenLiveHasNoGapOrDuplicate(t *testing.T) {
	h := NewHub(8, 4, 8)
	h.Publish(Decision{ID: 1})
	sub, err := h.Subscribe(Filter{}, ptr(0))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	h.Publish(Decision{ID: 2})
	if !eqIDs(ids(sub.Replay), []uint64{1}) {
		t.Fatalf("replay %v", ids(sub.Replay))
	}
	if d := recv(t, sub.C); d.ID != 2 {
		t.Fatalf("live got %d", d.ID)
	}
}

func TestHubSlowClientDroppedWithoutBlocking(t *testing.T) {
	h := NewHub(4, 4, 2)
	slow, err := h.Subscribe(Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := h.Subscribe(Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := uint64(1); i <= 10; i++ {
			h.Publish(Decision{ID: i})
			recv(t, fast.C)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a client that never reads")
	}
	// The slow client sees its buffer, then the close that says it was dropped.
	recv(t, slow.C)
	recv(t, slow.C)
	if _, ok := <-slow.C; ok {
		t.Fatal("slow client was not disconnected")
	}
	if h.Subscribers() != 1 || h.Dropped() != 1 {
		t.Fatalf("subscribers %d dropped %d", h.Subscribers(), h.Dropped())
	}
}

func TestHubCapsClients(t *testing.T) {
	h := NewHub(4, 2, 2)
	a, _ := h.Subscribe(Filter{}, nil)
	b, _ := h.Subscribe(Filter{}, nil)
	if _, err := h.Subscribe(Filter{}, nil); !errors.Is(err, ErrTooManyClients) {
		t.Fatalf("got %v", err)
	}
	a.Close()
	c, err := h.Subscribe(Filter{}, nil)
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	b.Close()
	c.Close()
}

func TestHubCloseEndsSubscribers(t *testing.T) {
	h := NewHub(4, 2, 2)
	sub, _ := h.Subscribe(Filter{}, nil)
	h.Close()
	if _, ok := <-sub.C; ok {
		t.Fatal("channel still open")
	}
	if _, err := h.Subscribe(Filter{}, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v", err)
	}
}
