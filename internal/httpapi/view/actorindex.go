package view

import (
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
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
	a.recent = append(a.recent, Decision{ID: r.Seq, At: int64(r.At), Kind: r.Kind, Actor: id, Intent: string(d.Intent.Kind), Scores: d.Scores, Clock: string(d.Clock)})
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
