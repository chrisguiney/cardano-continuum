package view

import (
	"fmt"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// totals are the run-wide counters the Overview reports.
type totals struct {
	lastSeq    uint64
	now        int64
	tip        *Point
	blocks     uint64
	rollbacks  uint64
	ada        Ada
	txs        TxCounts
	bodyErrors int
}

// Projection is the read model built by folding event log records in order.
// It is the only state the log-backed Source answers from.
type Projection struct {
	mu     sync.Mutex // protects every field below
	obs    func(eventlog.Record)
	tot    totals
	actors actorIndex
	// findings are the log's oracle findings, deduplicated by ID. The set
	// has its own lock, taken inside mu.
	findings *oracle.Set
	// meter folds the attack steps and the metrics Measure reads.
	meter *engine.Meter
	gov   *govFold
	// ctl folds the controls the engine applied.
	ctl *controlFold
}

// Observe sets f to be called with every record after it is folded, under
// the projection's lock: f must not call back into the projection.
func (p *Projection) Observe(f func(eventlog.Record)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.obs = f
}

// NewProjection returns an empty projection.
func NewProjection() *Projection {
	return &Projection{actors: newActorIndex(), findings: oracle.NewSet(), meter: engine.NewMeter(), gov: newGovFold(), ctl: newControlFold()}
}

// Apply folds one record. A body that does not decode is counted, shown as a
// failed invariant, and otherwise skipped: one bad record must not hide the
// rest of the run.
func (p *Projection) Apply(r eventlog.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tot.lastSeq = r.Seq
	p.tot.now = max(p.tot.now, int64(r.At))
	var err error
	if merr := p.meter.Add(r); merr != nil {
		p.tot.bodyErrors++
	}

	if cerr := p.foldControl(r); cerr != nil {
		p.tot.bodyErrors++
	}

	switch r.Class {
	case eventlog.Input:
		err = foldInput(p, r)
	case eventlog.Decision:
		err = foldDecision(p, r)
	}
	if err != nil {
		p.tot.bodyErrors++
	}
	if p.obs != nil {
		p.obs(r)
	}
}

// foldControl folds a Control input or a Controlled decision; the other
// folds still see the record, so the Controlled decision stays on actor 0's
// timeline.
func (p *Projection) foldControl(r eventlog.Record) error {
	switch {
	case r.Class == eventlog.Input && r.Kind == eventlog.KindControl:
		return p.ctl.input(r)
	case r.Class == eventlog.Decision && r.Kind == engine.KindControlled:
		return p.ctl.answer(r)
	}
	return nil
}

// Controls is the steering the engine has applied.
func (p *Projection) Controls() ControlState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ctl.view()
}

// Profile returns the profile an actor spawned with, or "".
func (p *Projection) Profile(id eventlog.ActorID) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.profile(uint64(id))
}

// NoteBodyError records a body the feed could not decode.
func (p *Projection) NoteBodyError() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tot.bodyErrors++
}

// UnknownProfile counts an actor no source names a profile for.
const UnknownProfile = "unknown"

// ProfileCounts counts every actor by profile, so the counts sum to the
// actor total. An actor the log spawned carries its profile; any other
// (genesis) actor takes profileOf's answer, and an empty answer counts as
// UnknownProfile. profileOf must not do I/O: it runs under the lock.
func (p *Projection) ProfileCounts(profileOf func(uint64) string) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.profileCounts(profileOf)
}

// Snapshot builds the Overview. ByProfile is left for ProfileCounts, which
// needs the store.
// A nil slots leaves Epoch unset.
func (p *Projection) Snapshot(slots *sched.Slots) Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := &p.tot
	s := Snapshot{
		LastSeq: t.lastSeq, SimTimeMs: t.now, Blocks: t.blocks, Rollbacks: t.rollbacks,
		Ada: t.ada, Txs: t.txs,
		Population: Population{Total: len(p.actors.actors), ByState: maps(p.actors.byState)},
		Invariants: []Invariant{{Name: "log_gapless", OK: true}, {Name: "bodies_decode", OK: t.bodyErrors == 0}},
	}
	if t.bodyErrors > 0 {
		s.Invariants[1].Detail = fmt.Sprintf("%d body decode failures", t.bodyErrors)
	}
	if t.tip != nil {
		tip := *t.tip
		s.Tip = &tip
	}
	if slots != nil {
		e := slots.EpochAt(sched.Time(t.now))
		s.Epoch = &e
	}
	if t.now > 0 {
		secs := float64(t.now) / 1000
		s.TxPerSec = float64(t.txs.Confirmed) / secs
		s.SubmitPerSec = float64(t.txs.Submitted) / secs
	}
	return s
}

func maps(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Governance builds the Governance view: life is the governance action
// lifetime in epochs, and slots, when set, dates proposals and gives their
// status. An actor the log did not spawn takes profileOf's answer, which
// may be nil and must not do I/O: it runs under the lock.
func (p *Projection) Governance(slots *sched.Slots, life uint64, profileOf ...func(uint64) string) GovernanceView {
	p.mu.Lock()
	defer p.mu.Unlock()
	var tip uint64
	if p.tot.tip != nil {
		tip = p.tot.tip.Slot
	}
	return p.gov.view(slots, tip, life, func(id uint64) string {
		if a := p.actors.actors[id]; a != nil && a.profile != "" {
			return a.profile
		}
		for _, f := range profileOf {
			return f(id)
		}
		return ""
	})
}

// Proposal builds one proposal's detail like Governance does, or reports
// ErrNotFound for an ID the chain does not carry.
func (p *Projection) Proposal(id string, slots *sched.Slots, life uint64, profileOf ...func(uint64) string) (ProposalDetail, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var tip uint64
	if p.tot.tip != nil {
		tip = p.tot.tip.Slot
	}
	d, ok := p.gov.detail(id, slots, tip, life, func(a uint64) string {
		if r := p.actors.actors[a]; r != nil && r.profile != "" {
			return r.profile
		}
		for _, f := range profileOf {
			return f(a)
		}
		return ""
	})
	if !ok {
		return ProposalDetail{}, fmt.Errorf("%w: governance action %s", ErrNotFound, id)
	}
	return d, nil
}

// Rows returns every actor's row, unsorted.
func (p *Projection) Rows() []ActorRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.rows()
}

// Timeline returns one actor's timeline, or ErrNotFound.
func (p *Projection) Timeline(id uint64) (Timeline, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.timeline(id)
}

// ActorLog returns the log's view of one actor, or ErrNotFound.
func (p *Projection) ActorLog(id uint64) (actorLog, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.log(id)
}

// TxOwner is the actor that submitted the transaction, if the log shows it.
func (p *Projection) TxOwner(tx []byte) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.actors.owner(tx)
}

// Attacks returns the meter's tallies and metrics.
func (p *Projection) Attacks() ([]engine.AttackStat, adversary.Metrics, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.meter.Attacks(), p.meter.Metrics(), p.tot.now
}

// attackOwner is the adversary that sent tx, by an Attack decision or by
// its own Submitted record.
func attackOwner(p *Projection, tx []byte) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.meter.AttackActor(tx); ok {
		return id, true
	}
	return p.actors.owner(tx)
}
