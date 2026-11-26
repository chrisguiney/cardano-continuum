package httpapi

import (
	"encoding/hex"
	"fmt"
	"slices"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
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
	return &Projection{actors: newActorIndex(), findings: oracle.NewSet(), meter: engine.NewMeter(), gov: newGovFold()}
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
func (p *Projection) attackOwner(tx []byte) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.meter.AttackActor(tx); ok {
		return id, true
	}
	return p.actors.owner(tx)
}

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

// actorLog is what the log knows about one actor.
type actorLog struct {
	info     actorInfo
	timeline []TimelineEntry
}

// actorIndex is the per-actor part of the projection: each actor's counters
// and timeline, the intents they own, and who submitted which transaction.
// It has no lock; Projection.mu guards it.
type actorIndex struct {
	actors  map[uint64]*actorInfo
	intents map[uint64]*intentInfo
	byState map[string]int
	// txOwner maps a submitted transaction to its actor, so a finding that
	// names a transaction can be shown on the actor.
	txOwner map[[32]byte]uint64
}

func newActorIndex() actorIndex {
	return actorIndex{
		actors: map[uint64]*actorInfo{}, intents: map[uint64]*intentInfo{},
		byState: map[string]int{}, txOwner: map[[32]byte]uint64{},
	}
}

func (x *actorIndex) actor(id uint64) *actorInfo {
	a := x.actors[id]
	if a == nil {
		a = &actorInfo{}
		x.actors[id] = a
	}
	return a
}

// note appends to an actor's timeline, dropping the oldest entries past the
// bound in batches so the cost stays amortised constant.
func (x *actorIndex) note(id uint64, r eventlog.Record, detail string, intent *uint64) {
	a := x.actor(id)
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

func (x *actorIndex) leave(state string) {
	if state == "" {
		return
	}
	if x.byState[state]--; x.byState[state] <= 0 {
		delete(x.byState, state)
	}
}

func (x *actorIndex) recordDecision(id uint64, r eventlog.Record, d agenda.Decision) {
	a := x.actor(id)
	a.recent = append(a.recent, Decision{ID: r.Seq, At: int64(r.At), Kind: r.Kind, Actor: id, Intent: string(d.Intent.Kind), Scores: d.Scores})
	if len(a.recent) > maxRecent {
		a.recent = slices.Delete(a.recent, 0, len(a.recent)-maxRecent)
	}
}

func (x *actorIndex) profile(id uint64) string {
	if a := x.actors[id]; a != nil {
		return a.profile
	}
	return ""
}

func (x *actorIndex) profileCounts(profileOf func(uint64) string) map[string]int {
	out := map[string]int{}
	for id, a := range x.actors {
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

func (x *actorIndex) rows() []ActorRow {
	out := make([]ActorRow, 0, len(x.actors))
	for id, a := range x.actors {
		out = append(out, ActorRow{
			ID: id, Profile: a.profile, State: a.state, Intents: a.intents, Txs: a.txs,
			Retries: a.retries, Confirmed: a.confirmed, Released: a.released,
		})
	}
	return out
}

func (x *actorIndex) timeline(id uint64) (Timeline, error) {
	a := x.actors[id]
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

func (x *actorIndex) log(id uint64) (actorLog, error) {
	a := x.actors[id]
	if a == nil {
		return actorLog{}, fmt.Errorf("%w: actor %d", ErrNotFound, id)
	}
	tl := a.tl[max(len(a.tl)-maxTimeline, 0):]
	c := *a
	c.recent, c.tl = slices.Clone(a.recent), nil
	return actorLog{info: c, timeline: slices.Clone(tl)}, nil
}

func (x *actorIndex) owner(tx []byte) (uint64, bool) {
	if len(tx) != 32 {
		return 0, false
	}
	id, ok := x.txOwner[[32]byte(tx)]
	return id, ok
}

// The fold functions apply one record to a Projection. The caller holds
// p.mu.

func foldInput(p *Projection, r eventlog.Record) error {
	switch r.Kind {
	case eventlog.KindBlockApplied:
		var b eventlog.BlockApplied
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		p.tot.tip = &Point{Slot: b.Point.Slot, Hash: hex.EncodeToString(b.Point.Hash)}
		p.tot.blocks++
		if len(b.Content) == 0 {
			return nil
		}
		var c engine.BlockContent
		if err := eventlog.Decode(b.Content, &c); err != nil {
			return err
		}
		for _, tx := range c.Txs {
			p.tot.ada.FeesLovelace += tx.Fee
		}
		p.gov.block(int64(r.At), b.Point.Slot, c)
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		p.tot.tip = &Point{Slot: b.To.Slot, Hash: hex.EncodeToString(b.To.Hash)}
		p.tot.rollbacks++
		p.gov.rollback(b.To.Slot)
	case eventlog.KindSubmitResult:
		var s engine.SubmitResult
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		if in := p.actors.intents[s.Intent]; in != nil {
			detail := verdictName(s.Verdict)
			if s.Reason != "" {
				detail += ": " + s.Reason
			}
			p.actors.note(in.actor, r, detail, &s.Intent)
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

func foldDecision(p *Projection, r eventlog.Record) error {
	id := uint64(r.Actor)
	switch r.Kind {
	case oracle.KindFinding:
		return p.findings.Load(r)
	case engine.KindSpawned:
		var s engine.Spawned
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		a := p.actors.actor(id)
		a.profile, a.born = s.Profile, int64(r.At)
		p.actors.note(id, r, s.Profile, nil)
	case engine.KindTransition:
		var t engine.Transition
		if err := eventlog.Decode(r.Body, &t); err != nil {
			return err
		}
		a := p.actors.actor(id)
		p.actors.leave(a.state)
		a.state, a.changed = t.To, int64(r.At)
		p.actors.byState[t.To]++
		p.actors.note(id, r, t.From+" -> "+t.To, nil)
	case engine.KindIntentChosen:
		var d agenda.Decision
		if err := eventlog.Decode(r.Body, &d); err != nil {
			return err
		}
		p.actors.actor(id).intents++
		p.actors.recordDecision(id, r, d)
		p.actors.note(id, r, string(d.Intent.Kind), nil)
	case engine.KindSubmitted:
		var s engine.Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		foldSubmitted(p, id, r, s)
	default:
		return foldOutcome(p, id, r)
	}
	return nil
}

func foldSubmitted(p *Projection, id uint64, r eventlog.Record, s engine.Submitted) {
	x := &p.actors
	p.gov.submitted(id, s)
	a := x.actor(id)
	a.txs++
	x.txOwner[[32]byte(s.ID)] = id
	p.tot.txs.Submitted++
	p.tot.ada.SubmittedLovelace += s.Amount
	in := x.intents[s.Intent]
	if in == nil {
		in = &intentInfo{actor: id}
		x.intents[s.Intent] = in
	}
	in.amount = s.Amount
	if in.attempts++; in.attempts > 1 {
		a.retries++
		p.tot.txs.Retries++
	}
	x.note(id, r, fmt.Sprintf("%d lovelace to %d", s.Amount, s.To), &s.Intent)
}

func foldOutcome(p *Projection, id uint64, r eventlog.Record) error {
	x := &p.actors
	switch r.Kind {
	case engine.KindConfirmed:
		var c engine.Confirmed
		if err := eventlog.Decode(r.Body, &c); err != nil {
			return err
		}
		x.actor(id).confirmed++
		x.actor(id).lost += c.Lost
		p.tot.txs.Confirmed++
		if in := x.intents[c.Intent]; in != nil {
			p.tot.ada.ConfirmedLovelace += in.amount
		}
		x.note(id, r, "", &c.Intent)
	case engine.KindRefused:
		var v engine.Refused
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		x.actor(id).refused++
		p.tot.txs.Refused++
		x.note(id, r, v.Why, &v.Intent)
	case engine.KindReleased:
		var v engine.Released
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		x.actor(id).released++
		p.tot.txs.Released++
		x.note(id, r, v.Why, &v.Intent)
	case engine.KindGaveUp:
		var v engine.GaveUp
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		x.actor(id).gaveUp++
		p.tot.txs.GaveUp++
		x.note(id, r, "", &v.Intent)
	case engine.KindSkipped:
		var v engine.Skipped
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		p.tot.txs.Skipped++
		x.note(id, r, v.Kind+": "+v.Why, nil)
	default:
		x.note(id, r, "", nil)
	}
	return nil
}
