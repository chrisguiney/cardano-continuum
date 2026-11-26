package oracle

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Answer is one node's answer to one send.
type Answer struct {
	Node    Node
	Outcome submit.Outcome
}

// Attempt is one send of a transaction and every node's answer to it.
type Attempt struct {
	// Seq is the log seq of the submit result.
	Seq uint64
	Tx  []byte
	ID  chain.TxID
	// Expected is mistake.Expect for the mistakes the attempt carries and
	// whether it, or one it conflicts with, was sent before (the submitter's
	// Machine.Sent). Nil means no expectation, as for a transaction an
	// adversary or a scenario wrote: only Differential applies then.
	Expected *mistake.Expected
	Answers  []Answer
}

// Judge compares every answer with the expected verdict set and returns a
// finding for each one that does not pass. An unreached send says nothing
// and passes.
func Judge(a Attempt, r Run) []Finding {
	if a.Expected == nil {
		return nil
	}
	var out []Finding
	for _, ans := range a.Answers {
		j, ok := judge(*a.Expected, ans.Outcome)
		if ok {
			continue
		}
		out = append(out, seal(verdictFinding(a, ans, j), r, a.Seq))
	}
	return out
}

// judgement is why an answer failed.
type judgement struct {
	kind       Kind
	sev        Severity
	unexpected []submit.Class
	detail     string
}

func judge(e mistake.Expected, o submit.Outcome) (judgement, bool) {
	switch o.Verdict {
	case submit.Unreached:
		return judgement{}, true
	case submit.Accepted:
		if e.Accept {
			return judgement{}, true
		}
		return judgement{kind: UnexpectedAccept, sev: Fatal}, false
	}
	if d, unknown := unknownDetail(o); unknown || len(o.Failures) == 0 {
		return judgement{kind: Unclassified, sev: Error, detail: d}, false
	}
	if e.Pass(o) {
		return judgement{}, true
	}
	var extra []submit.Class
	explained := false
	for _, f := range o.Failures {
		explained = explained || e.Allows(f.Class)
		if !e.Allows(f.Class) && !slices.Contains(extra, f.Class) {
			extra = append(extra, f.Class)
		}
	}
	slices.Sort(extra)
	// A rejection naming an expected class beside others was a rightful
	// rejection with a wrong class added; one naming none, of a transaction
	// the node may accept, refused it for no reason the oracle knows.
	if e.Accept && !explained {
		return judgement{kind: UnexpectedReject, sev: Error, unexpected: extra}, false
	}
	return judgement{kind: WrongClass, sev: Warn, unexpected: extra}, false
}

// unknownDetail joins the node's text for every unmapped failure.
func unknownDetail(o submit.Outcome) (string, bool) {
	var ds []string
	for _, f := range o.Failures {
		if f.Class == submit.Unknown {
			ds = append(ds, f.Detail)
		}
	}
	return strings.Join(ds, "; "), len(ds) > 0
}

func verdictFinding(a Attempt, ans Answer, j judgement) Finding {
	e := expectation(*a.Expected)
	got := verdictText(ans.Outcome)
	facts := []Fact{
		key("tx", hex.EncodeToString(a.ID[:])),
		key("impl", ans.Node.Impl),
		key("verdict", got),
		note("pool", ans.Node.Pool),
		note("expected", e.String()),
	}
	if len(j.unexpected) > 0 {
		facts = append(facts, note("unexpected", classList(j.unexpected)))
	}
	if j.detail != "" {
		facts = append(facts, note("detail", j.detail))
	}
	return Finding{
		Kind: j.kind, Severity: j.sev,
		Summary: fmt.Sprintf("%s on %s %s tx %s; expected %s",
			ans.Node.Impl, ans.Node.Pool, got, short(a.ID), e),
		Tx: a.Tx, TxHash: slices.Clone(a.ID[:]), Expected: &e, Verdicts: verdicts(a.Answers), Facts: facts,
		Nodes: []Node{ans.Node},
	}
}

// Differential reports two implementations splitting on the same send: one
// accepted it while another rejected it naming a rule that reads only the
// transaction and the protocol parameters (submit.Intrinsic). A rejection
// naming only state-dependent classes beside an acceptance is what an
// implementation answers when the transaction, or one it conflicts with,
// reached it first, so it is not a split. Pools of one implementation are
// not compared with each other.
func Differential(a Attempt, r Run) (Finding, bool) {
	var acc, rej []Answer
	for _, ans := range a.Answers {
		switch {
		case ans.Outcome.Verdict == submit.Accepted:
			acc = append(acc, ans)
		case ans.Outcome.Verdict == submit.Rejected && intrinsic(ans.Outcome):
			rej = append(rej, ans)
		}
	}
	accImpls, rejImpls := impls(acc), impls(rej)
	if !split(accImpls, rejImpls) {
		return Finding{}, false
	}
	var nodes []Node
	for _, ans := range append(acc, rej...) {
		nodes = append(nodes, ans.Node)
	}
	f := Finding{
		Kind: VerdictSplit, Severity: Fatal,
		Summary: fmt.Sprintf("tx %s accepted by %s, rejected by %s",
			short(a.ID), strings.Join(accImpls, ","), strings.Join(rejImpls, ",")),
		Tx: a.Tx, TxHash: slices.Clone(a.ID[:]), Verdicts: verdicts(a.Answers), Nodes: nodes,
		Facts: []Fact{
			key("tx", hex.EncodeToString(a.ID[:])),
			key("accepted_by", strings.Join(accImpls, ",")),
			key("rejected_by", strings.Join(rejImpls, ",")),
		},
	}
	if a.Expected != nil {
		e := expectation(*a.Expected)
		f.Expected = &e
	}
	return seal(f, r, a.Seq), true
}

func intrinsic(o submit.Outcome) bool {
	return slices.ContainsFunc(o.Failures, func(f submit.Failure) bool { return submit.Intrinsic(f.Class) })
}

// impls are the sorted distinct implementations of answers.
func impls(as []Answer) []string {
	var out []string
	for _, a := range as {
		if !slices.Contains(out, a.Node.Impl) {
			out = append(out, a.Node.Impl)
		}
	}
	slices.Sort(out)
	return out
}

// split reports an implementation on one side that differs from one on the
// other.
func split(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x != y {
				return true
			}
		}
	}
	return false
}

func expectation(e mistake.Expected) Expectation {
	return Expectation{Classes: slices.Clone(e.Classes), Accept: e.Accept}
}

func verdicts(as []Answer) []Verdict {
	var out []Verdict
	for _, a := range as {
		v := Verdict{Node: a.Node, Verdict: verdictName(a.Outcome.Verdict)}
		for _, f := range a.Outcome.Failures {
			v.Classes = append(v.Classes, f.Class)
		}
		v.Detail, _ = unknownDetail(a.Outcome)
		out = append(out, v)
	}
	return out
}

func verdictName(v submit.Verdict) string {
	switch v {
	case submit.Accepted:
		return "accepted"
	case submit.Rejected:
		return "rejected"
	}
	return "unreached"
}

// verdictText is the verdict with its distinct classes sorted, so the same
// answer under another rule order is the same finding.
func verdictText(o submit.Outcome) string {
	if o.Verdict != submit.Rejected {
		return verdictName(o.Verdict)
	}
	var cs []submit.Class
	for _, f := range o.Failures {
		cs = append(cs, f.Class)
	}
	slices.Sort(cs)
	return strings.TrimSpace("rejected " + classList(slices.Compact(cs)))
}

func short(id chain.TxID) string { return hex.EncodeToString(id[:4]) }

func uitoa(v uint64) string { return strconv.FormatUint(v, 10) }
