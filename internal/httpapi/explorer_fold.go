package httpapi

import (
	"cmp"
	"encoding/hex"
	"slices"
	"strconv"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

// submission is what the log says about a submitted transaction.
type submission struct {
	actor  uint64
	intent uint64
	kind   string
	amount uint64
	attack string
	// validator names the script the transaction pays to or runs.
	validator string
	// scripts are its runs as the engine evaluated them.
	scripts []contract.Run
}

type verdict struct {
	name   string
	reason string
}

// txFold folds the run's own records into what the chain cannot say about a
// transaction: who submitted it and why, the node's verdict, and the oracle's
// findings. It is fed under the projection's lock, so it never calls back.
type txFold struct {
	mu       sync.Mutex // protects bytx, verdicts
	bytx     map[[32]byte]submission
	verdicts map[uint64]verdict
	findings *oracle.Set
	// scripts folds the script runs into the Contracts view; it has its
	// own lock.
	scripts *scriptFold
}

func newTxFold() *txFold {
	return &txFold{bytx: map[[32]byte]submission{}, verdicts: map[uint64]verdict{}, findings: oracle.NewSet(), scripts: newScriptFold()}
}

// apply folds one record; a record of another kind, or one that does not
// decode, is skipped.
func (f *txFold) apply(r eventlog.Record) {
	f.scripts.apply(r)
	switch {
	case r.Class == eventlog.Decision && r.Kind == engine.KindSubmitted:
		var s engine.Submitted
		if eventlog.Decode(r.Body, &s) != nil {
			return
		}
		f.mu.Lock()
		f.bytx[[32]byte(s.ID)] = submission{actor: uint64(r.Actor), intent: s.Intent, kind: s.Kind, amount: s.Amount, attack: s.Attack, validator: s.Validator, scripts: s.Scripts}
		f.mu.Unlock()
	case r.Class == eventlog.Input && r.Kind == eventlog.KindSubmitResult:
		var s engine.SubmitResult
		if eventlog.Decode(r.Body, &s) != nil {
			return
		}
		f.mu.Lock()
		f.verdicts[s.Intent] = verdict{name: view.VerdictName(s.Verdict), reason: s.Reason}
		f.mu.Unlock()
	case r.Class == eventlog.Decision && r.Kind == oracle.KindFinding:
		_ = f.findings.Load(r)
	}
}

// of returns the submission and verdict recorded for tx.
func (f *txFold) of(tx [32]byte) (submission, verdict, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.bytx[tx]
	return s, f.verdicts[s.intent], ok
}

// findingsFor lists the oracle findings that name tx.
func (f *txFold) findingsFor(tx [32]byte) []TxFinding {
	out := []TxFinding{}
	all, _ := f.findings.Page(0, f.findings.Len())
	for _, fd := range all {
		if hex.EncodeToString(fd.TxHash) == hex.EncodeToString(tx[:]) {
			out = append(out, TxFinding{ID: fd.ID, Kind: string(fd.Kind), Severity: fd.Severity.String(), Summary: fd.Summary})
		}
	}
	return out
}

// Bounds on what the Contracts view keeps per validator, so a long run's
// fold stays small: the newest runs, the distinct failure reasons, and the
// submitted transactions remembered to tell a resend from a new run.
const (
	lastRuns    = 10
	maxReasons  = 20
	seenTxs     = 4096
	maxInstance = 10_000
)

// tally is what the fold keeps of one validator's runs.
type tally struct {
	// runs and failures count the runs and the failed ones.
	runs, failures int
	// used and declared sum the units, for the means.
	used, declared contract.Units
	// peakUsed and peakDeclared are the largest of each.
	peakUsed, peakDeclared contract.Units
	// reasons count failures by reason, at most maxReasons of them.
	reasons map[string]int
	// last are the newest runs, oldest first, at most lastRuns.
	last []RecentRun
	// instances are the distinct instances seen.
	instances map[string]bool
}

// scriptFold folds Submitted and Deployed records into the Contracts
// view. It is fed under the projection's lock, so it never calls back.
type scriptFold struct {
	mu sync.Mutex // protects byName, seen, order
	// byName is each validator's tally.
	byName map[string]*tally
	// seen are the recently submitted transactions, so a resend of the
	// same bytes is not counted again.
	seen map[[32]byte]bool
	// order is seen's transactions oldest first, to evict the oldest.
	order [][32]byte
}

func newScriptFold() *scriptFold {
	return &scriptFold{byName: map[string]*tally{}, seen: map[[32]byte]bool{}}
}

func (f *scriptFold) tally(name string) *tally {
	t, ok := f.byName[name]
	if !ok {
		t = &tally{reasons: map[string]int{}, instances: map[string]bool{}}
		f.byName[name] = t
	}
	return t
}

// apply folds one record; any other record, or one that does not decode,
// is skipped.
func (f *scriptFold) apply(r eventlog.Record) {
	if r.Class != eventlog.Decision {
		return
	}

	switch r.Kind {
	case engine.KindDeployed:
		var d engine.Deployed
		if eventlog.Decode(r.Body, &d) != nil || d.Validator == "" {
			return
		}
		f.mu.Lock()
		f.tally(d.Validator).instance(d.Address)
		f.mu.Unlock()
	case engine.KindSubmitted:
		var s engine.Submitted
		if eventlog.Decode(r.Body, &s) != nil || len(s.Scripts) == 0 {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.first([32]byte(s.ID)) {
			return
		}

		for _, run := range s.Scripts {
			if run.Validator != "" {
				f.tally(run.Validator).add(hex.EncodeToString(s.ID[:]), int64(r.At), run)
			}
		}
	}
}

// first reports whether tx is new to the fold, and remembers it.
func (f *scriptFold) first(tx [32]byte) bool {
	if f.seen[tx] {
		return false
	}

	f.seen[tx] = true
	f.order = append(f.order, tx)
	if len(f.order) > seenTxs {
		delete(f.seen, f.order[0])
		f.order = f.order[1:]
	}
	return true
}

func (t *tally) instance(name string) {
	if name != "" && len(t.instances) < maxInstance {
		t.instances[name] = true
	}
}

func (t *tally) add(tx string, at int64, run contract.Run) {
	t.runs++
	t.used.Mem, t.used.Steps = t.used.Mem+run.Used.Mem, t.used.Steps+run.Used.Steps
	t.declared.Mem, t.declared.Steps = t.declared.Mem+run.Declared.Mem, t.declared.Steps+run.Declared.Steps
	t.peakUsed = contract.Units{Mem: max(t.peakUsed.Mem, run.Used.Mem), Steps: max(t.peakUsed.Steps, run.Used.Steps)}
	t.peakDeclared = contract.Units{Mem: max(t.peakDeclared.Mem, run.Declared.Mem), Steps: max(t.peakDeclared.Steps, run.Declared.Steps)}
	t.instance(cmp.Or(run.Instance, run.Validator))

	if !run.OK {
		t.failures++
		if _, ok := t.reasons[run.Error]; ok || len(t.reasons) < maxReasons {
			t.reasons[run.Error]++
		}
	}

	t.last = append(t.last, RecentRun{
		Tx:       tx,
		At:       at,
		Instance: run.Instance,
		Purpose:  string(run.Purpose),
		OK:       run.OK,
		Used:     run.Used,
		Declared: run.Declared,
		Error:    run.Error,
	})
	if len(t.last) > lastRuns {
		t.last = t.last[1:]
	}
}

// view renders the fold over the registry.
func (f *scriptFold) view() ContractsView {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := ContractsView{Toolchain: contract.Toolchain}
	for _, v := range contract.All() {
		out.Validators = append(out.Validators, row(v, f.byName[v.Name]))
	}
	return out
}

func row(v contract.Validator, t *tally) ValidatorRow {
	purposes := make([]string, len(v.Purposes))
	for i, p := range v.Purposes {
		purposes[i] = string(p)
	}

	r := ValidatorRow{
		Name:     v.Name,
		Module:   v.Module,
		Hash:     v.Hash.String(),
		Size:     v.Size(),
		Language: "plutus_v" + strconv.Itoa(int(v.Language)),
		Purposes: purposes,
		Params:   v.Params,
		Datum:    v.Datum,
		Redeemer: v.Redeemer,
		Reasons:  []Reason{},
		Last:     []RecentRun{},
	}
	if t == nil {
		return r
	}

	r.Instances, r.Runs, r.Failures = len(t.instances), t.runs, t.failures
	if t.runs > 0 {
		n := int64(t.runs)
		r.Used = UnitStats{AvgMem: t.used.Mem / n, AvgSteps: t.used.Steps / n, MaxMem: t.peakUsed.Mem, MaxSteps: t.peakUsed.Steps}
		r.Declared = UnitStats{AvgMem: t.declared.Mem / n, AvgSteps: t.declared.Steps / n, MaxMem: t.peakDeclared.Mem, MaxSteps: t.peakDeclared.Steps}
	}

	for reason, n := range t.reasons {
		r.Reasons = append(r.Reasons, Reason{Reason: reason, Count: n})
	}
	slices.SortFunc(r.Reasons, func(a, b Reason) int { return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Reason, b.Reason)) })

	for i := len(t.last) - 1; i >= 0; i-- {
		r.Last = append(r.Last, t.last[i])
	}
	return r
}
