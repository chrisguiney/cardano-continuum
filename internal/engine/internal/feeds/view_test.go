package feeds

import (
	"context"
	"log/slog"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type fixedClock sched.Time

func (c fixedClock) Now() sched.Time { return sched.Time(c) }

// lagLoop is a loop whose engine time is behind the run's clock, as it is
// between a block input and the tick that catches the engine up.
type lagLoop struct {
	now  sched.Time
	recs []eventlog.Record
}

func (l *lagLoop) Feed(_ context.Context, r eventlog.Record) error {
	l.recs = append(l.recs, r)
	return nil
}
func (l *lagLoop) Effects(context.Context) error { return nil }
func (l *lagLoop) Now() sched.Time               { return l.now }

type oneViewer struct{}

func (oneViewer) View(context.Context, chain.Point, []common.Address) (Answer, error) {
	return Answer{Node: oracle.Node{Impl: "test"}}, nil
}

type settledAt struct{}

func (settledAt) Confirmed() eventlog.ChainPoint {
	return eventlog.ChainPoint{Slot: 5, Hash: make([]byte, 32)}
}
func (settledAt) Addresses() []common.Address                     { return nil }
func (settledAt) Owner(common.Address) (population.ActorID, bool) { return 0, false }

type branches struct{}

func (branches) Branches(context.Context) []oracle.Branch {
	return []oracle.Branch{{Node: oracle.Node{Pool: "p"}}}
}

// A node's answer is logged at the run's clock, as blocks are, not at the
// engine's last tick: the monitors judge it by that time (the stake
// floor's current epoch), and an earlier one puts the epoch behind a
// block the view already applied.
func TestMonitorSnapshotsStampedWithTheRunClock(t *testing.T) {
	t.Parallel()
	l := &lagLoop{now: 1000}
	clock := fixedClock(9000)
	if err := NewView(oneViewer{}, settledAt{}, clock, 0, slog.New(slog.DiscardHandler)).Poll(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	if err := NewTips(branches{}, clock, 0).Poll(context.Background(), l); err != nil {
		t.Fatal(err)
	}

	if len(l.recs) != 2 || l.recs[0].At != 9000 || l.recs[1].At != 9000 {
		t.Fatalf("records %+v, want two at the clock's 9000", l.recs)
	}
}
