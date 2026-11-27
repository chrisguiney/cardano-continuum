package scenario

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// SpecDisagreement is the kind of a finding where a node's verdict on a
// transaction is not the one the model gives it.
const SpecDisagreement oracle.Kind = "spec_disagreement"

// Target is one network a scenario's steps run against.
type Target interface {
	Name() string
	// Slot is the slot the next submission is validated at.
	Slot() uint64
	Submit(ctx context.Context, tx []byte) submit.Outcome
	// AdvanceTo returns once the target has reached slot.
	AdvanceTo(ctx context.Context, slot uint64) error
	// Inject applies an environment fault; only a fake chain can.
	Inject(f fakechain.Fault) error
}

// Backend opens targets. The fake chain is one; a devnet is another.
type Backend interface {
	// Env is the backend's part of a run's environment: parameters,
	// network and capabilities.
	Env() Env
	// Open returns a target that meets pre.
	Open(ctx context.Context, pre Preconditions) (Target, error)
}

// Status is how a scenario ended.
type Status string

const (
	// Pass: every step got the model's verdict.
	Pass Status = "pass"
	// Disagreement: some step got another verdict; see Findings.
	Disagreement Status = "spec_disagreement"
	// Unconcretizable: no step could be carried to the target; see Reason.
	Unconcretizable Status = "unconcretizable"
)

// Result is one scenario's outcome.
type Result struct {
	ID     string
	Family string
	Source Source
	Status Status
	// Reason says why an Unconcretizable scenario could not run.
	Reason string
	// Steps is the scenario's abstract step count, Ran how many were
	// submitted.
	Steps, Ran int
	Skipped    []Skip
	Truncated  *Skip
	Findings   []oracle.Finding
	// Notes are rejections that agree with the model's verdict but name a
	// failure the family's mapping does not list.
	Notes []string
}

// Run concretizes s for b's target and executes it. An error is a failure of
// the scenario text or the backend, never a verdict.
func Run(ctx context.Context, s Scenario, b Backend, seed rng.Seed) (Result, error) {
	res := Result{ID: s.ID, Family: s.Family, Source: s.Source, Steps: s.steps()}
	a, err := AdapterFor(s)
	if err != nil {
		return res, err
	}
	return runWith(ctx, s, a, b, seed)
}

func runWith(ctx context.Context, s Scenario, a Adapter, b Backend, seed rng.Seed) (Result, error) {
	res := Result{ID: s.ID, Family: s.Family, Source: s.Source, Steps: s.steps()}
	env := b.Env()
	env.Seed = seed
	var un *Unmappable
	pre, err := a.Preconditions(s, env)
	if errors.As(err, &un) {
		return res.unconcretizable(un.Reason), nil
	}
	if err != nil {
		return res, err
	}
	t, err := b.Open(ctx, pre)
	if err != nil {
		return res, fmt.Errorf("open target for %s: %w", s.ID, err)
	}
	env.Start = t.Slot()
	plan, err := a.Concretize(s, env)
	if errors.As(err, &un) {
		return res.unconcretizable(un.Reason), nil
	}
	if err != nil {
		return res, err
	}
	res.Skipped, res.Truncated = plan.Skipped, plan.Truncated
	x := executor{s: s, t: t, res: &res}
	for _, st := range plan.Steps {
		done, err := x.step(ctx, st)
		if err != nil {
			return res, fmt.Errorf("%s: %s: %w", s.ID, st.Note, err)
		}
		if done {
			break
		}
	}
	return res.settle(), nil
}

func (r Result) unconcretizable(reason string) Result {
	r.Status, r.Reason = Unconcretizable, reason
	return r
}

// settle decides the status once the steps are over.
func (r Result) settle() Result {
	switch {
	case len(r.Findings) > 0:
		r.Status = Disagreement
	case r.Ran > 0:
		r.Status = Pass
	case r.Reason != "":
		r.Status = Unconcretizable
	case r.Truncated != nil:
		return r.unconcretizable(r.Truncated.Reason)
	case len(r.Skipped) > 0:
		return r.unconcretizable(r.Skipped[0].Reason)
	default:
		return r.unconcretizable("no step to run")
	}
	return r
}

// executor runs a plan's steps against one target.
type executor struct {
	s   Scenario
	t   Target
	res *Result
}

// step runs one step and reports whether the scenario is over.
func (x executor) step(ctx context.Context, st Step) (bool, error) {
	switch st.Kind {
	case Wait:
		return false, x.t.AdvanceTo(ctx, st.Slot)
	case Fault:
		return false, x.t.Inject(st.Fault)
	case Submit:
		return x.submit(ctx, st)
	}
	return false, fmt.Errorf("unknown step kind %q", st.Kind)
}

func (x executor) submit(ctx context.Context, st Step) (bool, error) {
	if got := x.t.Slot(); got != st.Slot {
		return false, fmt.Errorf("target validates at slot %d, the step was written for slot %d", got, st.Slot)
	}
	out := x.t.Submit(ctx, st.Tx)
	if out.Verdict == submit.Unreached {
		// The target gave no verdict: the fake refuses what it does not
		// model. Nothing after a setup or an accepted step can run.
		x.res.Reason = fmt.Sprintf("target gave no verdict on step %d: %v", st.Index, out.Err)
		return st.Setup || st.Expect.Accept, nil
	}
	if st.Setup {
		if out.Verdict != submit.Accepted {
			x.res.Reason = "precondition transaction rejected: " + failureText(out)
			return true, nil
		}
		return false, nil
	}
	x.res.Ran++
	att := oracle.Attempt{
		Seq: uint64(st.Index), Tx: st.Tx, ID: chain.TxID(st.ID), Expected: &st.Expect,
		Answers: []oracle.Answer{{Node: oracle.Node{Impl: x.t.Name()}, Outcome: out}},
	}
	stop := false
	for _, f := range oracle.Judge(att, oracle.Run{}) {
		verdict := f.Kind == oracle.UnexpectedAccept || f.Kind == oracle.UnexpectedReject
		strict := st.Strict && f.Kind == oracle.WrongClass
		if !verdict && !strict {
			x.res.Notes = append(x.res.Notes, fmt.Sprintf("step %d: %s: %s", st.Index, f.Kind, failureText(out)))
			continue
		}
		x.res.Findings = append(x.res.Findings, disagreement(x.s, st, f))
		// Later steps were written for the state the model's verdict
		// leaves; the target is no longer in it.
		stop = true
	}
	return stop, nil
}

func failureText(o submit.Outcome) string {
	text := ""
	for i, f := range o.Failures {
		if i > 0 {
			text += "; "
		}
		text += string(f.Class)
		if f.Detail != "" {
			text += " (" + f.Detail + ")"
		}
	}
	return text
}

// disagreement turns the verdict oracle's finding into a spec_disagreement
// carrying the scenario's provenance. Its ID hashes the scenario, the step,
// the target and what the target answered, so a rerun gives the same ID.
func disagreement(s Scenario, st Step, f oracle.Finding) oracle.Finding {
	answer := string(f.Kind)
	f.Facts = append([]oracle.Fact{
		{Name: "scenario", Value: s.ID, Key: true},
		{Name: "step", Value: strconv.Itoa(st.Index), Key: true},
		{Name: "answer", Value: answer, Key: true},
		{Name: "repo", Value: s.Prov.Repo},
		{Name: "commit", Value: s.Prov.Commit},
		{Name: "spec", Value: s.Prov.Spec},
		{Name: "cfg", Value: s.Prov.Cfg},
		{Name: "invariant", Value: s.Prov.Invariant},
	}, f.Facts...)
	f.Summary = fmt.Sprintf("%s step %d (%s): %s", s.ID, st.Index, s.Prov.Spec, f.Summary)
	f.Kind, f.Severity = SpecDisagreement, oracle.Error
	h := sha256.New()
	for _, part := range []string{string(SpecDisagreement), s.ID, strconv.Itoa(st.Index), answer} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	for _, n := range f.Nodes {
		h.Write([]byte(n.Impl))
		h.Write([]byte{0})
	}
	f.ID = hex.EncodeToString(h.Sum(nil)[:16])
	return f
}
