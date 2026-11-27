package judge

import (
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

type journal struct{ recs []eventlog.Record }

func (j *journal) Seq() uint64                { return 1 }
func (j *journal) Ends() []oracle.End         { return nil }
func (j *journal) Append(rec eventlog.Record) { j.recs = append(j.recs, rec) }

func TestWithClassesSortedAndUnique(t *testing.T) {
	t.Parallel()
	base := mistake.Expected{Classes: []submit.Class{"b"}}
	got := WithClasses(base, "c", "a", "b")
	if want := []submit.Class{"a", "b", "c"}; !slices.Equal(got.Classes, want) {
		t.Fatalf("classes %v, want %v", got.Classes, want)
	}
	if len(base.Classes) != 1 {
		t.Fatalf("WithClasses changed its argument: %v", base.Classes)
	}
}

func TestAnswerOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   wire.Answer
		want submit.Verdict
		nf   int
	}{
		{"accepted", wire.Answer{Verdict: wire.VerdictAccepted}, submit.Accepted, 0},
		{"rejected", wire.Answer{Verdict: wire.VerdictRejected, Failures: []string{"x", "y"}}, submit.Rejected, 2},
		{"unreached", wire.Answer{}, submit.Unreached, 0},
	}
	for _, tt := range tests {
		o := AnswerOutcome(tt.in)
		if o.Verdict != tt.want || len(o.Failures) != tt.nf {
			t.Errorf("%s: %+v", tt.name, o)
		}
	}
}

func TestBlockBudget(t *testing.T) {
	t.Parallel()
	units := func(m, s int64) *common.ExUnits { return &common.ExUnits{Memory: m, Steps: s} }
	j := New(Spec{Build: build.Params{MaxBlockExUnits: common.ExUnits{Memory: 10, Steps: 10}}}, Env{})
	j.Block(eventlog.ChainPoint{}, []wire.TxDigest{{Units: units(3, 4)}, {}, {Units: units(2, 1)}})
	if b := j.budget; b == nil || b.Used.Memory != 5 || b.Used.Steps != 5 || b.Txs != 3 {
		t.Fatalf("budget %+v", b)
	}
	j.Begin()
	if j.budget != nil {
		t.Fatal("Begin kept the block")
	}
	j = New(Spec{}, Env{})
	j.Block(eventlog.ChainPoint{}, nil)
	if j.budget != nil {
		t.Fatal("a run without a block budget set one")
	}
}

func TestReportLogsFirstOccurrenceOnly(t *testing.T) {
	t.Parallel()
	jr := &journal{}
	j := New(Spec{}, Env{Journal: jr})
	f := oracle.Finding{ID: "f1", Kind: oracle.Kind("k")}
	for range 2 {
		if err := j.Report(0, []oracle.Finding{f}); err != nil {
			t.Fatal(err)
		}
	}
	if len(jr.recs) != 1 {
		t.Fatalf("%d records, want 1", len(jr.recs))
	}
}

func TestLost(t *testing.T) {
	t.Parallel()
	j := New(Spec{}, Env{})
	j.AddLost(3)
	j.AddLost(4)
	if j.Lost() != 7 {
		t.Fatalf("lost %d", j.Lost())
	}
	j.Load(1)
	if j.Lost() != 1 {
		t.Fatalf("lost %d after restore", j.Lost())
	}
}
