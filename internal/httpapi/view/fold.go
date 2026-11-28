package view

import (
	"encoding/hex"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

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
			detail := VerdictName(s.Verdict)
			if s.Reason != "" {
				detail += ": " + s.Reason
			}
			p.actors.note(in.actor, r, detail, &s.Intent)
		}
	}
	return nil
}

// VerdictName names a submit verdict as the feed shows it.
func VerdictName(v engine.Verdict) string {
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
	case engine.KindQuote:
		// A Quote is the market's, not an actor's.
		return nil
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
	case engine.KindProtocol:
		return p.gov.protocol(r)
	case engine.KindTreasury:
		return p.gov.treasury.move(r)
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
	case engine.KindControlled, engine.KindDeployed, engine.KindIncident:
		// Engine-wide records: logged under actor 0, they belong to no
		// actor, so they must not create one.
	default:
		x.note(id, r, "", nil)
	}
	return nil
}
