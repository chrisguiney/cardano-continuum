package engine

import (
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/judge"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// judgeSpec is what the judge component is built from; genesis is the
// lovelace value conservation counts from.
func judgeSpec(e *Engine, genesis uint64) judge.Spec {
	cfg := &e.cfg
	return judge.Spec{
		Mistakes: cfg.Mistakes, Build: cfg.Build, Run: cfg.Run, K: cfg.K, BlockEvery: cfg.BlockEvery,
		Guard: e.fl.Guard(), Genesis: genesis, Faucet: faucetActor, Again: intent.Again,
	}
}

// judgeEnv is the rest of the engine as the judge component reaches it.
func judgeEnv(e *Engine) judge.Env {
	return judge.Env{Roster: e.co, Ring: e.co.Ring, Chain: e.win, Journal: &e.jr, Ledger: jhooks{e}, Floor: jhooks{e}, Race: jhooks{e}, Forks: jhooks{e}}
}

// jhooks is the supply, the stake floor and the contested outputs as the
// judge reaches them.
type jhooks struct{ e *Engine }

// Supply implements judge.Ledger.
func (h jhooks) Supply() uint64 { return h.e.Summary().Supply }

// Balances implements judge.Ledger.
func (h jhooks) Balances() map[uint64]uint64 { return h.e.pay.Balances() }

// Holders implements judge.Ledger.
func (h jhooks) Holders() oracle.Holders { return h.e.pay.Reads() }

// State implements judge.Floor.
func (h jhooks) State(at sched.Time) policy.State { return h.e.fl.State(at) }

// VersionAt implements judge.Forks.
func (h jhooks) VersionAt(ep uint64) (protocol.Version, bool) {
	v := h.e.win.View()
	return v.VersionAt(ep), ep <= v.Epoch()
}

// Contested implements judge.Race.
func (h jhooks) Contested(ref wire.Ref) bool { return h.e.adv.Contested(ref) }
