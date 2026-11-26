package clock

import (
	"errors"
	"slices"
	"testing"
)

// Same-time events come out sorted by (Kind, Actor, Birth), whatever the
// push order, and the clock follows the batch.
func TestNextSortsSameTimeEvents(t *testing.T) {
	var c Clock
	c.Push(5, Event{Kind: Act, Actor: 2})
	c.Push(5, Event{Kind: Birth, Birth: 1})
	c.Push(5, Event{Kind: Act, Actor: 1})
	c.Push(5, Event{Kind: Birth, Birth: 0})
	c.Push(9, Event{Kind: Hazard})
	if err := c.Open(7); err != nil {
		t.Fatal(err)
	}
	at, batch, ok := c.Next(7)
	want := []Event{{Kind: Birth, Birth: 0}, {Kind: Birth, Birth: 1}, {Kind: Act, Actor: 1}, {Kind: Act, Actor: 2}}
	if !ok || at != 5 || !slices.Equal(batch, want) || c.Now() != 5 {
		t.Fatalf("Next = %d %v %v, now %d; want 5 %v", at, batch, ok, c.Now(), want)
	}
	if _, _, ok := c.Next(7); ok || c.Now() != 7 {
		t.Fatalf("after the batch: ok %v, now %d, want false, 7", ok, c.Now())
	}
	if next, ok := c.Peek(); !ok || next != 9 {
		t.Fatalf("Peek = %d %v, want the event at 9", next, ok)
	}
}

func TestOpenRefusesBackwards(t *testing.T) {
	var c Clock
	c.Restore(10)
	if err := c.Open(9); !errors.Is(err, ErrBackwards) {
		t.Fatalf("Open(9) = %v, want ErrBackwards", err)
	}
	if err := c.Open(12); err != nil || c.TickTo() != 12 {
		t.Fatalf("Open(12) = %v, tickTo %d", err, c.TickTo())
	}
}

func TestCancelRemovesAnEvent(t *testing.T) {
	var c Clock
	h := c.Push(3, Event{Kind: Act})
	c.Cancel(h)
	if _, ok := c.Peek(); ok {
		t.Fatal("a cancelled event is still scheduled")
	}
}
