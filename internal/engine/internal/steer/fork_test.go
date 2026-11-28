package steer

import (
	"context"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func TestForkDecide(t *testing.T) {
	t.Parallel()
	v10, v11 := protocol.Version{Major: 10}, protocol.Version{Major: 11}
	plan := protocol.Schedule{To: v11, Epoch: 3}
	for _, c := range []struct {
		name      string
		plan      protocol.Schedule
		epoch     uint64
		cur       protocol.Version
		committee bool
		pools     bool
		want      ForkMove
	}{
		{"no schedule", protocol.Schedule{}, 9, v10, true, true, ForkWait},
		{"already there", plan, 9, v11, true, true, ForkWait},
		{"keys first, before the epoch", plan, 0, v10, false, true, ForkAuthorise},
		{"keys first, at the epoch", plan, 3, v10, false, true, ForkAuthorise},
		{"early", plan, 2, v10, true, true, ForkWait},
		{"no pool stake", plan, 3, v10, true, false, ForkWait},
		{"ready", plan, 3, v10, true, true, ForkPropose},
		{"late", plan, 9, v10, true, true, ForkPropose},
		// From 11 a committee vote carries its own authorisation.
		{"no keys needed from 11", protocol.Schedule{To: protocol.Version{Major: 12}, Epoch: 0}, 1, v11, false, true, ForkPropose},
	} {
		if got := ForkDecide(c.plan, c.epoch, c.cur, c.committee, c.pools); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

type fakeFork struct {
	epoch     uint64
	cur       protocol.Version
	committee bool
	pools     bool
	calls     []string
	armed     []sched.Time
}

func (f *fakeFork) Epoch() uint64             { return f.epoch }
func (f *fakeFork) Version() protocol.Version { return f.cur }
func (f *fakeFork) CommitteeReady() bool      { return f.committee }
func (f *fakeFork) PoolsReady() bool          { return f.pools }
func (f *fakeFork) Arm(at sched.Time)         { f.armed = append(f.armed, at) }
func (f *fakeFork) HotKeys(context.Context, sched.Time) (string, error) {
	f.calls = append(f.calls, "hot_keys")
	return "", nil
}

func (f *fakeFork) HardFork(_ context.Context, _ sched.Time, arg string) (string, error) {
	f.calls = append(f.calls, "hard_fork:"+arg)
	return "", nil
}

// A step re-arms itself first, then authorises or proposes as decided, and
// asks the hard_fork scenario for the scheduled version.
func TestForkStep(t *testing.T) {
	t.Parallel()
	plan := protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 3}
	for _, c := range []struct {
		f    fakeFork
		want []string
	}{
		{fakeFork{epoch: 1, cur: protocol.Version{Major: 10}, pools: true}, []string{"hot_keys"}},
		{fakeFork{epoch: 3, cur: protocol.Version{Major: 10}, committee: true, pools: true}, []string{"hard_fork:11.0"}},
		{fakeFork{epoch: 3, cur: protocol.Version{Major: 10}, committee: true}, nil},
	} {
		f := c.f
		if err := ForkStep(context.Background(), 5, plan, &f); err != nil {
			t.Fatal(err)
		}

		if len(f.armed) != 1 || f.armed[0] != 5 {
			t.Errorf("armed %v, want one re-arm from 5", f.armed)
		}

		if len(f.calls) != len(c.want) || (len(c.want) == 1 && f.calls[0] != c.want[0]) {
			t.Errorf("calls %v, want %v", f.calls, c.want)
		}
	}
}
