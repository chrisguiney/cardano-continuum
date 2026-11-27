package engine

import (
	"encoding/hex"
	"fmt"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/btcsuite/btcd/btcutil/bech32"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// An actor's row carries a store.Identity: its public addresses and keys,
// traits, and the engine's own view of its holdings and delegation. The
// store keeps it opaque, so the web API can answer from a checkpoint
// without the run's seed. Nothing here touches private key material: only
// public hashes and addresses are read from the wallet.

// holding is one actor's share of the planner ledger and the chain view.
type holding struct {
	settled, reserved, unconfirmed uint64
	utxos, inflight                int
	tokens                         map[tokenKey]uint64
}

type tokenKey struct {
	policy common.Blake2b224
	name   string
}

// addTokens adds the native assets of output u to h.
func (h *holding) addTokens(u common.Utxo) {
	ma := u.Output.Assets()
	if ma == nil {
		return
	}
	for _, pol := range ma.Policies() {
		for _, name := range ma.Assets(pol) {
			if h.tokens == nil {
				h.tokens = map[tokenKey]uint64{}
			}
			if q := ma.Asset(pol, name); q != nil {
				h.tokens[tokenKey{pol, string(name)}] += q.Uint64()
			}
		}
	}
}

// tokenList is h's tokens in policy then name order.
func tokenList(h *holding) []store.Token {
	out := make([]store.Token, 0, len(h.tokens))
	for k, q := range h.tokens {
		t := store.Token{Policy: hex.EncodeToString(k.policy[:]), Name: hex.EncodeToString([]byte(k.name)), Quantity: q}
		if utf8.ValidString(k.name) && printable(k.name) {
			t.NameText = k.name
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b store.Token) int {
		if a.Policy != b.Policy {
			return compareStrings(a.Policy, b.Policy)
		}
		return compareStrings(a.Name, b.Name)
	})
	return out
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func printable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return s != ""
}

// bech32Of encodes data under hrp; the inputs are fixed-size hashes.
func bech32Of(hrp string, data []byte) string {
	conv, err := bech32.ConvertBits(data, 8, 5, true)
	if err != nil {
		// Unreachable: eight to five bits with padding accepts any input.
		panic("engine: bech32 convert: " + err.Error())
	}
	s, err := bech32.Encode(hrp, conv)
	if err != nil {
		panic("engine: bech32 encode: " + err.Error())
	}
	return s
}

// The actor kinds kindOf reports beyond the profile kinds.
const (
	kindAdversary = "adversary"
	kindProposer  = "proposer"
)

// ActorKinds returns every value kindOf can report: the profile kinds plus
// the proposer and adversary refinements.
func ActorKinds() []string {
	out := []string{kindAdversary, kindProposer}
	for _, k := range profile.Kinds() {
		out = append(out, string(k))
	}
	slices.Sort(out)
	return out
}

// idents builds the identity rows.
type idents struct {
	r *roster
	// proposer is the profile name of the network proposer.
	proposer string
	att      attackHooks
	view     chainView
}

// kindOf names an actor's role for the UI: a profile kind, refined for the
// network proposer and for adversaries.
func (n *idents) kindOf(id population.ActorID, p profile.Profile) string {
	switch {
	case n.att.Adversary(id):
		return kindAdversary
	case p.Name == n.proposer:
		return kindProposer
	}
	return string(p.Kind)
}

// staticIdentity is the part of an actor's identity that never changes,
// computed once per actor.
func (n *idents) staticIdentity(id population.ActorID, st *actor) store.Identity {
	w := st.wallet
	pk, sk := w.PaymentKeyHash(), w.StakeKeyHash()
	out := store.Identity{
		Kind: n.kindOf(id, st.prof), Agenda: string(st.prof.Agenda),
		PaymentAddr: w.Address().String(), StakeAddr: w.StakeAddress().String(),
		PaymentKeyHash: hex.EncodeToString(pk[:]), StakeKeyHash: hex.EncodeToString(sk[:]),
	}
	if st.prof.Kind == profile.DRep {
		d := n.r.ring.drep(id)
		out.DRepID = bech32Of("drep", d[:])
	}
	if pool, ok := n.r.ring.poolOf(id); ok {
		out.PoolID = bech32Of("pool", pool[:])
	}
	if ad := st.prof.Adversary; ad != nil {
		out.CostCeiling, out.Metric = uint64(ad.CostCeiling), ad.SuccessMetric
		out.Budget, out.Attacks = uint64(st.prof.Budget), slices.Clone(ad.Attacks)
	}
	return out
}

// identityOf is the actor's identity now.
func (n *idents) identityOf(a population.Actor, st *actor, hs map[population.ActorID]*holding) store.Identity {
	if !st.identOK {
		st.ident, st.identOK = n.staticIdentity(a.ID, st), true
	}
	out := st.ident
	out.Competence, out.Care = a.Competence, a.Traits.Care
	out.Patience, out.LearningRate = a.Traits.Patience, a.Traits.LearningRate
	out.Frustration = a.Frustration
	if a.Kind == profile.Wallet {
		out.Hazard = population.DormancyOnsetRate(st.prof.Hazards.OnsetRate(), a.Frustration)
	}
	if h := hs[a.ID]; h != nil {
		out.Settled, out.UTxOs, out.Reserved = h.settled, h.utxos, h.reserved
		out.InFlight, out.Unconfirmed = h.inflight, h.unconfirmed
		out.Tokens = tokenList(h)
	}
	if n.att.Adversary(a.ID) {
		out.CostSpent = n.att.Spent(a.ID)
	}
	n.delegationOf(&out, a.ID)
	return out
}

func (n *idents) delegationOf(out *store.Identity, id population.ActorID) {
	s, ok := n.view.View().StakeOf(id)
	if !ok {
		return
	}
	out.StakeRegistered = s.Registered
	if s.Delegated {
		out.DelegatedPool = bech32Of("pool", s.Pool[:])
		if pa, ok := n.view.View().PoolActor(s.Pool); ok {
			out.DelegatedActor = uint64(pa)
		}
	}
	if s.Votes {
		out.DelegatedDRep = drepText(s.DRep)
	}
}

func drepText(d chain.DRep) string {
	switch d.Kind {
	case common.DrepTypeAddrKeyHash:
		return bech32Of("drep", d.Cred[:])
	case common.DrepTypeScriptHash:
		return bech32Of("drep_script", d.Cred[:])
	case common.DrepTypeAbstain:
		return "always_abstain"
	case common.DrepTypeNoConfidence:
		return "always_no_confidence"
	}
	return fmt.Sprintf("drep_kind_%d", d.Kind)
}
