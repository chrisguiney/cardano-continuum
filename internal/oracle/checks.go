package oracle

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"
	"maps"
	"math/bits"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
)

// checkValue: utxo + fees + deposits + rewards + treasury + reserves equals
// the genesis total plus the inflow. The key is the discrepancy, so one fault seen at every
// later snapshot is one finding.
func checkValue(s Snapshot) []Finding {
	v := s.Value
	if v == nil {
		return nil
	}
	var sum, carry, c uint64
	for _, p := range []uint64{v.Utxo, v.Fees, v.Deposits, v.Rewards, v.Treasury, v.Reserves} {
		sum, c = bits.Add64(sum, p, 0)
		carry |= c
	}
	want, c := bits.Add64(v.Genesis, v.Inflow, 0)
	carry |= c
	var delta string
	switch {
	case carry != 0:
		delta = "overflow"
	case sum == want:
		return nil
	case sum < want:
		delta = "-" + uitoa(want-sum)
	default:
		delta = uitoa(sum - want)
	}
	return []Finding{{
		Kind: ValueNotConserved, Severity: Fatal,
		Summary: fmt.Sprintf("accounted lovelace differs from the genesis total %d by %s at slot %d", v.Genesis, delta, v.At.Slot),
		Facts: []Fact{
			key("delta", delta), note("genesis", uitoa(v.Genesis)), note("utxo", uitoa(v.Utxo)),
			note("fees", uitoa(v.Fees)), note("deposits", uitoa(v.Deposits)), note("rewards", uitoa(v.Rewards)),
			note("treasury", uitoa(v.Treasury)), note("reserves", uitoa(v.Reserves)), note("inflow", uitoa(v.Inflow)), note("at", pointText(v.At)),
		},
	}}
}

// checkPaid: an intent ends at most once. Two distinct attempts settling, or
// one settling after the planner wrote the intent off, pays it twice
// (IntentAtMostOnce, DeadIsDead).
func checkPaid(s Snapshot) []Finding {
	type ends struct {
		txs      []chain.TxID
		released bool
		seqs     []string
	}
	by := map[uint64]*ends{}
	for _, e := range s.Ends {
		x := by[e.Intent]
		if x == nil {
			x = &ends{}
			by[e.Intent] = x
		}
		x.seqs = append(x.seqs, uitoa(e.Seq))
		if e.Released {
			x.released = true
		} else if !slices.Contains(x.txs, e.Tx) {
			x.txs = append(x.txs, e.Tx)
		}
	}
	var out []Finding
	for _, id := range slices.Sorted(maps.Keys(by)) {
		x := by[id]
		if len(x.txs) < 2 && (len(x.txs) == 0 || !x.released) {
			continue
		}
		txs := make([]string, len(x.txs))
		for i, t := range x.txs {
			txs[i] = hex.EncodeToString(t[:])
		}
		out = append(out, Finding{
			Kind: PaidTwice, Severity: Fatal,
			Summary: fmt.Sprintf("intent %d settled by %d attempts (written off: %v)", id, len(x.txs), x.released),
			Facts: []Fact{key("intent", uitoa(id)), note("txs", strings.Join(txs, ",")),
				note("released", fmt.Sprint(x.released)), note("seqs", strings.Join(x.seqs, ","))},
		})
	}
	return out
}

// checkReservations: an output is held by at most one intent, every input of
// an unfinished attempt is held by its intent (ReservedWhileUnfinished), and
// every hold is for an unfinished attempt of its intent spending it (NoLeak).
func checkReservations(s Snapshot) []Finding {
	r := s.Reservations
	if r == nil {
		return nil
	}
	claims := map[chain.Ref][]plan.IntentID{}
	held := map[chain.Ref]bool{}
	for _, h := range r.Holds {
		held[h.Ref] = true
		claims[h.Ref] = appendNew(claims[h.Ref], h.Intent)
	}
	spends := map[Hold]bool{}
	for _, p := range r.Unfinished {
		for _, in := range p.Inputs {
			spends[Hold{Ref: in, Intent: p.Intent}] = true
			claims[in] = appendNew(claims[in], p.Intent)
		}
	}
	var out []Finding
	for _, ref := range sortedRefs(claims) {
		ids := claims[ref]
		switch {
		case len(ids) > 1:
			out = append(out, reservationFinding(ReservationDouble, Fatal, ref, ids, "held for more than one intent"))
		case !held[ref]:
			out = append(out, reservationFinding(Unreserved, Fatal, ref, ids, "spent by an unfinished attempt without a reservation"))
		}
	}
	for _, h := range r.Holds {
		if !spends[h] && len(claims[h.Ref]) == 1 {
			out = append(out, reservationFinding(ReservationLeaked, Error, h.Ref, []plan.IntentID{h.Intent}, "held with no unfinished attempt of its intent spending it"))
		}
	}
	return out
}

func appendNew(ids []plan.IntentID, id plan.IntentID) []plan.IntentID {
	if slices.Contains(ids, id) {
		return ids
	}
	return append(ids, id)
}

func sortedRefs(m map[chain.Ref][]plan.IntentID) []chain.Ref {
	refs := slices.Collect(maps.Keys(m))
	slices.SortFunc(refs, func(a, b chain.Ref) int {
		return cmp.Or(bytes.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
	})
	return refs
}

func reservationFinding(k Kind, sev Severity, ref chain.Ref, ids []plan.IntentID, why string) Finding {
	ids = slices.Sorted(slices.Values(ids))
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = uitoa(uint64(id))
	}
	r := fmt.Sprintf("%s#%d", hex.EncodeToString(ref.Tx[:]), ref.Index)
	return Finding{
		Kind: k, Severity: sev,
		Summary: fmt.Sprintf("output %s#%d %s (intents %s)", short(ref.Tx), ref.Index, why, strings.Join(s, ",")),
		Facts:   []Fact{key("ref", r), key("intent", strings.Join(s, ","))},
	}
}

// checkSettled: the settled point is a high-water mark. A move to an older
// slot, or to another block at the same slot, undid a settled block.
func checkSettled(s Snapshot) []Finding {
	m := s.Settled
	if m == nil || m.To.Slot > m.From.Slot || m.To == m.From {
		return nil
	}
	return []Finding{{
		Kind: SettledBack, Severity: Fatal,
		Summary: fmt.Sprintf("settled point moved from %s back to %s", pointText(m.From), pointText(m.To)),
		Facts:   []Fact{key("from", pointText(m.From)), key("to", pointText(m.To))},
	}}
}

// checkViews: at a point both answered for, the engine's balance of each
// actor equals the node's.
func checkViews(s Snapshot) []Finding {
	var out []Finding
	for _, v := range s.Views {
		if v.At != v.NodeAt {
			continue
		}
		ids := slices.Collect(maps.Keys(v.Engine))
		for id := range v.Chain {
			if _, ok := v.Engine[id]; !ok {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		for _, id := range ids {
			e, n := v.Engine[id], v.Chain[id]
			if e == n {
				continue
			}
			out = append(out, Finding{
				Kind: ViewMismatch, Severity: Error,
				Summary: fmt.Sprintf("actor %d: engine has %d lovelace, %s on %s has %d at %s",
					id, e, v.Node.Impl, v.Node.Pool, n, pointText(v.At)),
				Facts: []Fact{key("pool", v.Node.Pool), key("actor", uitoa(uint64(id))),
					key("engine", uitoa(e)), key("node", uitoa(n)), note("at", pointText(v.At))},
				Nodes: []Node{v.Node},
			})
		}
	}
	return out
}

// checkProtocol: every node is at the protocol version the ratified hard
// forks put in force in the epoch it answered for. The engine's view moves
// the version at the boundary after the one that ratified the fork, as the
// ledger enacts it (models/specs/governance-change HardFork.tla,
// VersionAgrees). The key leaves the epoch out, so a node stuck on the old
// version is one finding, however many answers show it.
func checkProtocol(s Snapshot) []Finding {
	var out []Finding
	for _, v := range s.Views {
		if v.Version == nil || v.Want == nil || *v.Version == *v.Want {
			continue
		}
		out = append(out, Finding{
			Kind: ProtocolMismatch, Severity: Fatal,
			Summary: fmt.Sprintf("%s on %s is at protocol version %s in epoch %d; the ratified hard forks put the chain at %s",
				v.Node.Impl, v.Node.Pool, v.Version, v.Epoch, v.Want),
			Facts: []Fact{key("pool", v.Node.Pool), key("node", v.Version.String()), key("want", v.Want.String()),
				note("epoch", uitoa(v.Epoch))},
			Nodes: []Node{v.Node},
		})
	}
	return out
}

// checkLiveness: an epoch passed with no block, so the ledger never runs
// another epoch transition (stake-floor NoFreeze, contract rule 14).
func checkLiveness(s Snapshot) []Finding {
	p := s.Policy
	if p == nil || !p.Frozen() {
		return nil
	}
	return []Finding{{
		Kind: Frozen, Severity: Fatal,
		Summary: fmt.Sprintf("epoch %d had no block: the ledger is frozen at epoch %d's transition", p.Label+1, p.Label),
		Facts:   []Fact{key("epoch", uitoa(p.Label+1)), note("now", uitoa(p.Epoch))},
	}}
}

// checkFloor: the pools that really forge hold the floor in every schedule
// already on its way (policy.Guard.Endangered, FloorHolds).
func checkFloor(s Snapshot, g policy.Guard) []Finding {
	p := s.Policy
	if p == nil || !g.Endangered(*p) {
		return nil
	}
	var forging []string
	for _, pl := range p.Pools {
		if pl.Reg == policy.Registered && pl.Live && pl.Running {
			forging = append(forging, uitoa(uint64(pl.ID)))
		}
	}
	return []Finding{{
		Kind: FloorBreached, Severity: Error,
		Summary: fmt.Sprintf("epoch %d: forging pools hold less than the stake floor in a schedule on its way", p.Epoch),
		Facts:   []Fact{key("epoch", uitoa(p.Epoch)), note("label", uitoa(p.Label)), note("forging", strings.Join(forging, ","))},
	}}
}

// checkTips compares every pair of pools of a network (Ouroboros Praos'
// common prefix: no honest node rolls back more than k blocks). A fork whose
// both sides hold more than k blocks past the fork point can never resolve:
// neither side may switch. A pool behind another on the same chain is lag,
// not a fork.
func checkTips(s Snapshot, k uint64) []Finding {
	var out []Finding
	for i, a := range s.Tips {
		for _, b := range s.Tips[i+1:] {
			if f, ok := fork(a, b, k); ok {
				out = append(out, f)
			}
		}
	}
	return out
}

func fork(a, b Branch, k uint64) (Finding, bool) {
	if len(a.Points) == 0 || len(b.Points) == 0 {
		return Finding{}, false
	}
	at := make(map[chain.Point]int, len(a.Points))
	for i, p := range a.Points {
		at[p] = i
	}
	da, db, common := uint64(len(a.Points)), uint64(len(b.Points)), "before both windows"
	found := false
	for j := len(b.Points) - 1; j >= 0; j-- {
		if i, ok := at[b.Points[j]]; ok {
			da, db = uint64(len(a.Points)-1-i), uint64(len(b.Points)-1-j)
			common, found = pointText(b.Points[j]), true
			break
		}
	}
	// Windows sharing no block are a fork only if they overlap in time;
	// otherwise one pool is simply too far behind to compare.
	if !found && !overlap(a.Points, b.Points) {
		return Finding{}, false
	}
	if da <= k || db <= k {
		return Finding{}, false
	}
	pools := []string{a.Node.Pool, b.Node.Pool}
	slices.Sort(pools)
	facts := []Fact{key("pools", strings.Join(pools, ","))}
	if found {
		facts = append(facts, note("fork_slot", strings.SplitN(common, "/", 2)[0]))
	}
	facts = append(facts, note("fork_point", common),
		note("tip_"+a.Node.Pool, pointText(a.Points[len(a.Points)-1])),
		note("tip_"+b.Node.Pool, pointText(b.Points[len(b.Points)-1])),
		note("depth_"+a.Node.Pool, uitoa(da)), note("depth_"+b.Node.Pool, uitoa(db)))
	return Finding{
		Kind: DeepFork, Severity: Fatal,
		Summary: fmt.Sprintf("%s (%s) and %s (%s) forked %s, %d and %d blocks deep, past k = %d",
			a.Node.Pool, a.Node.Impl, b.Node.Pool, b.Node.Impl, common, da, db, k),
		Facts: facts, Nodes: []Node{a.Node, b.Node},
	}, true
}

func overlap(a, b []chain.Point) bool {
	return a[0].Slot <= b[len(b)-1].Slot && b[0].Slot <= a[len(a)-1].Slot
}

func pointText(p chain.Point) string {
	if p.IsOrigin() {
		return "origin"
	}
	return uitoa(p.Slot) + "/" + hex.EncodeToString(p.Hash[:])
}

// checkRogue reports every rogue block found on a node's chain. Each node
// that holds one is its own finding: the block is invalid for all of them,
// and each implementation's acceptance is a separate fault.
func checkRogue(s Snapshot) []Finding {
	if len(s.Rogue) == 0 {
		return nil
	}
	bad := make(map[chain.Point]RogueBlock, len(s.Rogue))
	for _, r := range s.Rogue {
		bad[r.Point] = r
	}
	var out []Finding
	for _, b := range s.Tips {
		for _, p := range b.Points {
			r, ok := bad[p]
			if !ok {
				continue
			}
			out = append(out, Finding{
				Kind: RogueAdopted, Severity: Fatal,
				Summary: fmt.Sprintf("%s (%s) adopted the %s block %s, which breaks %s",
					b.Node.Pool, b.Node.Impl, r.Kind, pointText(p), r.Rule),
				Facts: []Fact{key("block", pointText(p)), key("pool", b.Node.Pool),
					note("impl", b.Node.Impl), note("kind", r.Kind), note("rule", r.Rule)},
				Nodes: []Node{b.Node}, Block: r.CBOR,
			})
		}
	}
	return out
}
