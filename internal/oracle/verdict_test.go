package oracle

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var run = Run{Seed: 7, Log: "out/events.log"}

func node(impl, pool string) Node {
	return Node{Impl: impl, Pool: pool, Digest: "sha256:" + impl, Revisions: map[string]string{impl: "abc123"}}
}

func accepted() submit.Outcome { return submit.Outcome{Verdict: submit.Accepted} }

func unreached() submit.Outcome { return submit.Outcome{Verdict: submit.Unreached} }

func rejected(cs ...submit.Class) submit.Outcome {
	o := submit.Outcome{Verdict: submit.Rejected}
	for _, c := range cs {
		o.Failures = append(o.Failures, submit.Failure{Class: c, Detail: "text of " + string(c)})
	}
	return o
}

func attempt(want *mistake.Expected, answers ...Answer) Attempt {
	return Attempt{Seq: 42, Tx: []byte{0x84, 0xa0}, ID: chain.TxID(bytes.Repeat([]byte{0xab}, 32)), Expected: want, Answers: answers}
}

func expect(ks mistake.KindSet, spent bool) *mistake.Expected {
	e := mistake.Expect(ks, spent)
	return &e
}

func kinds(fs []Finding) []Kind {
	var out []Kind
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func TestJudgeTable(t *testing.T) {
	t.Parallel()
	cn := node("cardano-node", "p1")
	tests := []struct {
		name string
		want *mistake.Expected
		got  submit.Outcome
		kind []Kind
	}{
		{"reject as expected", expect(mistake.Of(mistake.FeeTooSmall), false), rejected(submit.FeeTooSmall), nil},
		{"any expected subset", expect(mistake.Of(mistake.FeeTooSmall, mistake.Expired), false), rejected(submit.OutsideValidityInterval), nil},
		{"accept as expected", expect(0, false), accepted(), nil},
		{"no answer says nothing", expect(mistake.Of(mistake.FeeTooSmall), false), unreached(), nil},
		{"spent after send", expect(0, true), rejected(submit.MempoolFailure), nil},
		{"accepts a mistake", expect(mistake.Of(mistake.FeeTooSmall), false), accepted(), []Kind{UnexpectedAccept}},
		{"rejects a correct tx", expect(0, false), rejected(submit.FeeTooSmall), []Kind{UnexpectedReject}},
		{"names another class", expect(mistake.Of(mistake.FeeTooSmall), false), rejected(submit.FeeTooSmall, submit.ValueNotConserved), []Kind{WrongClass}},
		{"empty rejection", expect(mistake.Of(mistake.FeeTooSmall), false), rejected(), []Kind{Unclassified}},
		{"no expectation", nil, accepted(), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fs := Judge(attempt(tt.want, Answer{Node: cn, Outcome: tt.got}), run)
			if got := kinds(fs); !slices.Equal(got, tt.kind) {
				t.Fatalf("kinds = %v, want %v", got, tt.kind)
			}
			for _, f := range fs {
				checkVerdictFinding(t, f, cn)
			}
		})
	}
}

func checkVerdictFinding(t *testing.T, f Finding, blamed Node) {
	t.Helper()
	if f.ID == "" || f.Seq != 42 || !bytes.Equal(f.Tx, []byte{0x84, 0xa0}) || f.Expected == nil {
		t.Fatalf("finding lacks id, seq, tx or expectation: %+v", f)
	}
	if len(f.Nodes) != 1 || f.Nodes[0].Impl != blamed.Impl || f.Nodes[0].Digest != blamed.Digest || f.Nodes[0].Revisions[blamed.Impl] != "abc123" {
		t.Fatalf("nodes = %+v, want %v with its build", f.Nodes, blamed)
	}
	if f.Seed != 7 || f.Replay != "continuum replay --seed 7 out/events.log" || !f.Reproducible {
		t.Fatalf("seed %d replay %q reproducible %v", f.Seed, f.Replay, f.Reproducible)
	}
}

// A rejection a decoder could not map must be reported, never passed, even
// when the expected class is among the others: the node path decodes
// MissingVKeyWitnessesUTXOW as a generic error, so a missing signer comes
// back Unknown.
func TestJudgeUnknownIsUnclassified(t *testing.T) {
	t.Parallel()
	want := expect(mistake.Of(mistake.MissingSigner), false)
	for _, o := range []submit.Outcome{
		rejected(submit.Unknown),
		rejected(submit.MissingVKeyWitnesses, submit.Unknown),
	} {
		fs := Judge(attempt(want, Answer{Node: node("cardano-node", "p1"), Outcome: o}), run)
		if got := kinds(fs); !slices.Equal(got, []Kind{Unclassified}) {
			t.Fatalf("%v: kinds = %v, want unclassified", o.Failures, got)
		}
		if f := fs[0]; f.Severity != Error || !hasFact(f, "detail", "text of Unknown") {
			t.Fatalf("unclassified finding %+v lacks severity or the node's text", f)
		}
	}
}

// The fake chain adds MissingRedeemers to every BadInputs rejection, where a
// node names BadInputs alone. The oracle must report it against fakechain.
func TestJudgeFakeChainExtraClass(t *testing.T) {
	t.Parallel()
	fake := node("fakechain", "fakechain")
	fs := Judge(attempt(expect(mistake.Of(mistake.StaleInput), false),
		Answer{Node: fake, Outcome: rejected(submit.BadInputs, "MissingRedeemers")}), run)
	if got := kinds(fs); !slices.Equal(got, []Kind{WrongClass}) {
		t.Fatalf("kinds = %v", got)
	}
	if !hasFact(fs[0], "impl", "fakechain") || !hasFact(fs[0], "unexpected", "MissingRedeemers") {
		t.Fatalf("facts = %+v", fs[0].Facts)
	}
}

func hasFact(f Finding, name, value string) bool {
	return slices.Contains(f.Facts, Fact{Name: name, Value: value, Key: true}) ||
		slices.Contains(f.Facts, Fact{Name: name, Value: value})
}

func TestDifferentialSplit(t *testing.T) {
	t.Parallel()
	a := attempt(nil,
		Answer{Node: node("cardano-node", "p1"), Outcome: rejected(submit.FeeTooSmall)},
		Answer{Node: node("dingo", "p2"), Outcome: accepted()},
	)
	f, ok := Differential(a, run)
	if !ok || f.Kind != VerdictSplit || f.Severity != Fatal {
		t.Fatalf("split not reported: %+v %v", f, ok)
	}
	if len(f.Verdicts) != 2 || len(f.Nodes) != 2 || !bytes.Equal(f.Tx, a.Tx) {
		t.Fatalf("finding lacks both answers or the tx: %+v", f)
	}
}

func TestDifferentialAgreement(t *testing.T) {
	t.Parallel()
	for name, outs := range map[string][2]submit.Outcome{
		"both accept":        {accepted(), accepted()},
		"rule order differs": {rejected(submit.FeeTooSmall), rejected(submit.OutsideValidityInterval)},
		"one unreached":      {unreached(), rejected(submit.FeeTooSmall)},
	} {
		a := attempt(nil,
			Answer{Node: node("cardano-node", "p1"), Outcome: outs[0]},
			Answer{Node: node("dingo", "p2"), Outcome: outs[1]},
		)
		if f, ok := Differential(a, run); ok {
			t.Errorf("%s: unexpected split %+v", name, f)
		}
	}
	same := attempt(nil,
		Answer{Node: node("dingo", "p1"), Outcome: accepted()},
		Answer{Node: node("dingo", "p2"), Outcome: rejected(submit.FeeTooSmall)},
	)
	if _, ok := Differential(same, run); ok {
		t.Error("two pools of one implementation are not a differential")
	}
}

// An "inputs spent" rejection beside an acceptance is what an impl answers
// when the transaction reached it second; only a state-independent rule
// splitting the verdict is a disagreement.
func TestDifferentialIgnoresTiming(t *testing.T) {
	t.Parallel()
	for _, c := range []submit.Class{submit.BadInputs, submit.MempoolFailure} {
		a := attempt(nil,
			Answer{Node: node("cardano-node", "p1"), Outcome: accepted()},
			Answer{Node: node("dingo", "p2"), Outcome: rejected(c)},
		)
		if f, ok := Differential(a, run); ok {
			t.Errorf("%s: timing reported as split: %+v", c, f)
		}
	}
}

func TestFindingNonReproducibleFromDirty(t *testing.T) {
	t.Parallel()
	dirty := node("dingo", "p2")
	dirty.Dirty = true
	dirty.Revisions = map[string]string{"dingo": "abc123", "gouroboros": "def456"}
	a := attempt(expect(mistake.Of(mistake.FeeTooSmall), false),
		Answer{Node: node("cardano-node", "p1"), Outcome: rejected(submit.FeeTooSmall)},
		Answer{Node: dirty, Outcome: accepted()},
	)
	fs := Judge(a, run)
	f, ok := Differential(a, run)
	if !ok || len(fs) != 1 {
		t.Fatalf("judge %v, split %v", kinds(fs), ok)
	}
	for _, f := range append(fs, f) {
		if f.Reproducible {
			t.Errorf("%s from a dirty build is reproducible", f.Kind)
		}
		i := slices.IndexFunc(f.Nodes, func(n Node) bool { return n.Impl == "dingo" })
		if i < 0 || f.Nodes[i].Revisions["gouroboros"] != "def456" {
			t.Errorf("%s dropped the dirty node's revisions: %+v", f.Kind, f.Nodes)
		}
	}
	clean := attempt(a.Expected, Answer{Node: node("dingo", "p2"), Outcome: accepted()})
	if fs := Judge(clean, run); len(fs) != 1 || !fs[0].Reproducible {
		t.Fatalf("a clean build's finding is not reproducible: %+v", fs)
	}
}

// A node with no pool name (a live run's single node) must not leave a
// gap in the summary.
func TestSummaryWithoutPool(t *testing.T) {
	t.Parallel()
	got := Judge(attempt(expect(0, false), Answer{Node: Node{Impl: "node"}, Outcome: rejected(submit.FeeTooSmall)}), run)
	if len(got) != 1 {
		t.Fatalf("findings %v", kinds(got))
	}
	if strings.Contains(got[0].Summary, "  ") || strings.HasPrefix(got[0].Summary, "node on ") {
		t.Fatalf("summary %q", got[0].Summary)
	}
}

// Every rejected failure's text reaches the verdict, so a reader sees why
// a node refused, not only which rule.
func TestVerdictCarriesEveryFailureText(t *testing.T) {
	t.Parallel()
	got := Judge(attempt(expect(0, false), Answer{Node: node("cardano-node", "p1"), Outcome: rejected(submit.FeeTooSmall, submit.WrongNetwork)}), run)
	if len(got) != 1 || len(got[0].Verdicts) != 1 {
		t.Fatalf("findings %v", got)
	}
	d := got[0].Verdicts[0].Detail
	if !strings.Contains(d, "text of FeeTooSmallUTxO") || !strings.Contains(d, "text of WrongNetwork") {
		t.Fatalf("detail %q", d)
	}
}
