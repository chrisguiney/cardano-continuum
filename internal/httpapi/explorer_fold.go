package httpapi

import (
	"encoding/hex"
	"sync"

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
}

func newTxFold() *txFold {
	return &txFold{bytx: map[[32]byte]submission{}, verdicts: map[uint64]verdict{}, findings: oracle.NewSet()}
}

// apply folds one record; a record of another kind, or one that does not
// decode, is skipped.
func (f *txFold) apply(r eventlog.Record) {
	switch {
	case r.Class == eventlog.Decision && r.Kind == engine.KindSubmitted:
		var s engine.Submitted
		if eventlog.Decode(r.Body, &s) != nil {
			return
		}
		f.mu.Lock()
		f.bytx[[32]byte(s.ID)] = submission{actor: uint64(r.Actor), intent: s.Intent, kind: s.Kind, amount: s.Amount, attack: s.Attack}
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
