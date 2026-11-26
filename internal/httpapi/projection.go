package httpapi

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// maxTimeline bounds the entries kept per actor. A longer history drops its
// oldest entries and counts them.
const maxTimeline = 500

type actorInfo struct {
	profile   string
	state     string
	born      int64
	intents   int
	txs       int
	retries   int
	confirmed int
	released  int
	tl        []TimelineEntry
	dropped   int
	// The fields below feed the actor detail.
	refused, gaveUp int
	last, changed   int64
	lost            uint64
	recent          []Decision
}

type intentInfo struct {
	actor    uint64
	amount   uint64
	attempts int
}

// Projection is the read model built by folding event log records in order.
// It is the only state the log-backed Source answers from.
type Projection struct {
	mu         sync.Mutex // protects every field below
	lastSeq    uint64
	now        int64
	tip        *Point
	blocks     uint64
	rollbacks  uint64
	ada        Ada
	txs        TxCounts
	actors     map[uint64]*actorInfo
	intents    map[uint64]*intentInfo
	byState    map[string]int
	bodyErrors int
	// findings are the log's oracle findings, deduplicated by ID. The set
	// has its own lock, taken inside mu.
	findings *oracle.Set
	// txOwner maps a submitted transaction to its actor, so a finding that
	// names a transaction can be shown on the actor.
	txOwner map[[32]byte]uint64
	// meter folds the attack steps and the metrics Measure reads.
	meter *engine.Meter
	gov   *govFold
}

// NewProjection returns an empty projection.
func NewProjection() *Projection {
	return &Projection{
		actors: map[uint64]*actorInfo{}, intents: map[uint64]*intentInfo{},
		byState: map[string]int{}, findings: oracle.NewSet(), txOwner: map[[32]byte]uint64{}, meter: engine.NewMeter(), gov: newGovFold(),
	}
}

// Apply folds one record. A body that does not decode is counted, shown as a
// failed invariant, and otherwise skipped: one bad record must not hide the
// rest of the run.
func (p *Projection) Apply(r eventlog.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSeq = r.Seq
	p.now = max(p.now, int64(r.At))
	var err error
	if merr := p.meter.Add(r); merr != nil {
		p.bodyErrors++
	}
	switch r.Class {
	case eventlog.Input:
		err = p.applyInput(r)
	case eventlog.Decision:
		err = p.applyDecision(r)
	}
	if err != nil {
		p.bodyErrors++
	}
}

// Profile returns the profile an actor spawned with, or "".
func (p *Projection) Profile(id eventlog.ActorID) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a := p.actors[uint64(id)]; a != nil {
		return a.profile
	}
	return ""
}

// NoteBodyError records a body the feed could not decode.
func (p *Projection) NoteBodyError() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bodyErrors++
}

func (p *Projection) applyInput(r eventlog.Record) error {
	switch r.Kind {
	case eventlog.KindBlockApplied:
		var b eventlog.BlockApplied
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		p.tip = &Point{Slot: b.Point.Slot, Hash: hex.EncodeToString(b.Point.Hash)}
		p.blocks++
		if len(b.Content) == 0 {
			return nil
		}
		var c engine.BlockContent
		if err := eventlog.Decode(b.Content, &c); err != nil {
			return err
		}
		for _, tx := range c.Txs {
			p.ada.FeesLovelace += tx.Fee
		}
		p.gov.block(int64(r.At), b.Point.Slot, c)
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		p.tip = &Point{Slot: b.To.Slot, Hash: hex.EncodeToString(b.To.Hash)}
		p.rollbacks++
		p.gov.rollback(b.To.Slot)
	case eventlog.KindSubmitResult:
		var s engine.SubmitResult
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		if in := p.intents[s.Intent]; in != nil {
			detail := verdictName(s.Verdict)
			if s.Reason != "" {
				detail += ": " + s.Reason
			}
			p.note(in.actor, r, detail, &s.Intent)
		}
	}
	return nil
}

func verdictName(v engine.Verdict) string {
	switch v {
	case engine.VerdictAccepted:
		return "accepted"
	case engine.VerdictRejected:
		return "rejected"
	case engine.VerdictUnreached:
		return "unreached"
	}
	return "unknown"
}

func (p *Projection) actor(id uint64) *actorInfo {
	a := p.actors[id]
	if a == nil {
		a = &actorInfo{}
		p.actors[id] = a
	}
	return a
}

// note appends to an actor's timeline, dropping the oldest entries past the
// bound in batches so the cost stays amortised constant.
func (p *Projection) note(id uint64, r eventlog.Record, detail string, intent *uint64) {
	a := p.actor(id)
	a.last = int64(r.At)
	var in *uint64
	if intent != nil {
		v := *intent
		in = &v
	}
	a.tl = append(a.tl, TimelineEntry{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Detail: detail, Intent: in})
	if len(a.tl) > 2*maxTimeline {
		drop := len(a.tl) - maxTimeline
		a.dropped += drop
		a.tl = slices.Delete(a.tl, 0, drop)
	}
}

func (p *Projection) applyDecision(r eventlog.Record) error {
	id := uint64(r.Actor)
	switch r.Kind {
	case oracle.KindFinding:
		return p.findings.Load(r)
	case engine.KindSpawned:
		var s engine.Spawned
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		a := p.actor(id)
		a.profile, a.born = s.Profile, int64(r.At)
		p.note(id, r, s.Profile, nil)
	case engine.KindTransition:
		var t engine.Transition
		if err := eventlog.Decode(r.Body, &t); err != nil {
			return err
		}
		a := p.actor(id)
		p.leave(a.state)
		a.state, a.changed = t.To, int64(r.At)
		p.byState[t.To]++
		p.note(id, r, t.From+" -> "+t.To, nil)
	case engine.KindIntentChosen:
		var d agenda.Decision
		if err := eventlog.Decode(r.Body, &d); err != nil {
			return err
		}
		p.actor(id).intents++
		p.recordDecision(id, r, d)
		p.note(id, r, string(d.Intent.Kind), nil)
	case engine.KindSubmitted:
		var s engine.Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		p.submitted(id, r, s)
	default:
		return p.applyOutcome(id, r)
	}
	return nil
}

func (p *Projection) leave(state string) {
	if state == "" {
		return
	}
	if p.byState[state]--; p.byState[state] <= 0 {
		delete(p.byState, state)
	}
}

func (p *Projection) submitted(id uint64, r eventlog.Record, s engine.Submitted) {
	p.gov.submitted(id, s)
	a := p.actor(id)
	a.txs++
	p.txOwner[[32]byte(s.ID)] = id
	p.txs.Submitted++
	p.ada.SubmittedLovelace += s.Amount
	in := p.intents[s.Intent]
	if in == nil {
		in = &intentInfo{actor: id}
		p.intents[s.Intent] = in
	}
	in.amount = s.Amount
	if in.attempts++; in.attempts > 1 {
		a.retries++
		p.txs.Retries++
	}
	p.note(id, r, fmt.Sprintf("%d lovelace to %d", s.Amount, s.To), &s.Intent)
}

func (p *Projection) applyOutcome(id uint64, r eventlog.Record) error {
	switch r.Kind {
	case engine.KindConfirmed:
		var c engine.Confirmed
		if err := eventlog.Decode(r.Body, &c); err != nil {
			return err
		}
		p.actor(id).confirmed++
		p.actor(id).lost += c.Lost
		p.txs.Confirmed++
		if in := p.intents[c.Intent]; in != nil {
			p.ada.ConfirmedLovelace += in.amount
		}
		p.note(id, r, "", &c.Intent)
	case engine.KindRefused:
		var x engine.Refused
		if err := eventlog.Decode(r.Body, &x); err != nil {
			return err
		}
		p.actor(id).refused++
		p.txs.Refused++
		p.note(id, r, x.Why, &x.Intent)
	case engine.KindReleased:
		var x engine.Released
		if err := eventlog.Decode(r.Body, &x); err != nil {
			return err
		}
		p.actor(id).released++
		p.txs.Released++
		p.note(id, r, x.Why, &x.Intent)
	case engine.KindGaveUp:
		var x engine.GaveUp
		if err := eventlog.Decode(r.Body, &x); err != nil {
			return err
		}
		p.actor(id).gaveUp++
		p.txs.GaveUp++
		p.note(id, r, "", &x.Intent)
	case engine.KindSkipped:
		var x engine.Skipped
		if err := eventlog.Decode(r.Body, &x); err != nil {
			return err
		}
		p.txs.Skipped++
		p.note(id, r, x.Kind+": "+x.Why, nil)
	default:
		p.note(id, r, "", nil)
	}
	return nil
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
	out := map[string]int{}
	for id, a := range p.actors {
		prof := a.profile
		if prof == "" && profileOf != nil {
			prof = profileOf(id)
		}
		if prof == "" {
			prof = UnknownProfile
		}
		out[prof]++
	}
	return out
}

// Snapshot builds the Overview. ByProfile is left for ProfileCounts, which
// needs the store.
// A nil slots leaves Epoch unset.
func (p *Projection) Snapshot(slots *sched.Slots) Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Snapshot{
		LastSeq: p.lastSeq, SimTimeMs: p.now, Blocks: p.blocks, Rollbacks: p.rollbacks,
		Ada: p.ada, Txs: p.txs,
		Population: Population{Total: len(p.actors), ByState: maps(p.byState)},
		Invariants: []Invariant{{Name: "log_gapless", OK: true}, {Name: "bodies_decode", OK: p.bodyErrors == 0}},
	}
	if p.bodyErrors > 0 {
		s.Invariants[1].Detail = fmt.Sprintf("%d body decode failures", p.bodyErrors)
	}
	if p.tip != nil {
		t := *p.tip
		s.Tip = &t
	}
	if slots != nil {
		e := slots.EpochAt(sched.Time(p.now))
		s.Epoch = &e
	}
	if p.now > 0 {
		secs := float64(p.now) / 1000
		s.TxPerSec = float64(p.txs.Confirmed) / secs
		s.SubmitPerSec = float64(p.txs.Submitted) / secs
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
	if p.tip != nil {
		tip = p.tip.Slot
	}
	return p.gov.view(slots, tip, life, func(id uint64) string {
		if a := p.actors[id]; a != nil && a.profile != "" {
			return a.profile
		}
		for _, f := range profileOf {
			return f(id)
		}
		return ""
	})
}

// Rows returns every actor's row, unsorted.
func (p *Projection) Rows() []ActorRow {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ActorRow, 0, len(p.actors))
	for id, a := range p.actors {
		out = append(out, ActorRow{
			ID: id, Profile: a.profile, State: a.state, Intents: a.intents, Txs: a.txs,
			Retries: a.retries, Confirmed: a.confirmed, Released: a.released,
		})
	}
	return out
}

// Timeline returns one actor's timeline, or ErrNotFound.
func (p *Projection) Timeline(id uint64) (Timeline, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.actors[id]
	if a == nil {
		return Timeline{}, fmt.Errorf("%w: actor %d", ErrNotFound, id)
	}
	entries := a.tl
	if len(entries) > maxTimeline {
		entries = entries[len(entries)-maxTimeline:]
	}
	dropped := a.dropped + len(a.tl) - len(entries)
	return Timeline{
		Actor: id, Profile: a.profile, State: a.state, BornMs: a.born,
		Dropped: dropped, Entries: slices.Clone(entries),
	}, nil
}
