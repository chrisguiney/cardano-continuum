package cohort

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

// Holding is one actor's share of the planner ledger and the chain view.
type Holding struct {
	Settled, Reserved, Unconfirmed uint64
	UTxOs, InFlight                int
	tokens                         map[tokenKey]uint64
}

type tokenKey struct {
	policy common.Blake2b224
	name   string
}

// AddTokens adds the native assets of output u to h.
func (h *Holding) AddTokens(u common.Utxo) {
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
func tokenList(h *Holding) []store.Token {
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

// Idents builds the identity rows.
type Idents struct {
	r *Roster
	// proposer is the profile name of the network proposer.
	proposer string
	att      Adversaries
	chain    Chain
	// desk reads a market wallet's position.
	desk *Desk
	// money reads a wallet's owner and its household.
	money *Money
}

// kindOf names an actor's role for the UI: a profile kind, refined for the
// network proposer and for adversaries.
func (n *Idents) kindOf(id population.ActorID, p profile.Profile) string {
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
func (n *Idents) staticIdentity(id population.ActorID, st *Actor) store.Identity {
	w := st.Wallet
	pk, sk := w.PaymentKeyHash(), w.StakeKeyHash()

	var drep, poolID string
	if st.Prof.Kind == profile.DRep {
		d := n.r.Ring.DRep(id)
		drep = bech32Of("drep", d[:])
	}
	if pool, ok := n.r.Ring.PoolOf(id); ok {
		poolID = bech32Of("pool", pool[:])
	}

	var adv store.Adversary
	if ad := st.Prof.Adversary; ad != nil {
		adv = store.Adversary{
			CostCeiling: uint64(ad.CostCeiling),
			Metric:      ad.SuccessMetric,
			Budget:      uint64(st.Prof.Budget),
			Attacks:     slices.Clone(ad.Attacks),
		}
	}

	return store.Identity{
		Kind:   n.kindOf(id, st.Prof),
		Agenda: string(st.Prof.Agenda),
		Keys: store.Keys{
			PaymentAddr:    w.Address().String(),
			StakeAddr:      w.StakeAddress().String(),
			PaymentKeyHash: hex.EncodeToString(pk[:]),
			StakeKeyHash:   hex.EncodeToString(sk[:]),
			DRepID:         drep,
			PoolID:         poolID,
		},
		Adversary: adv,
	}
}

// Identity is the actor's identity now.
func (n *Idents) Identity(a population.Actor, st *Actor, hs map[population.ActorID]*Holding) store.Identity {
	if !st.IdentOK {
		st.Ident, st.IdentOK = n.staticIdentity(a.ID, st), true
	}
	static := st.Ident

	var hazard float64
	if a.Kind == profile.Wallet {
		hazard = population.DormancyOnsetRate(st.Prof.Hazards.OnsetRate(), a.Frustration)
	}

	var hold store.Holdings
	if h := hs[a.ID]; h != nil {
		hold = store.Holdings{
			Settled:     h.Settled,
			UTxOs:       h.UTxOs,
			Reserved:    h.Reserved,
			InFlight:    h.InFlight,
			Unconfirmed: h.Unconfirmed,
			Tokens:      tokenList(h),
		}
	}

	adv := static.Adversary
	if n.att.Adversary(a.ID) {
		adv.CostSpent = n.att.Spent(a.ID)
	}

	return store.Identity{
		Kind:   static.Kind,
		Agenda: static.Agenda,
		Keys:   static.Keys,
		Traits: store.Traits{
			Competence:   a.Competence,
			Care:         a.Traits.Care,
			Patience:     a.Traits.Patience,
			LearningRate: a.Traits.LearningRate,
			Frustration:  a.Frustration,
			Hazard:       hazard,
			LossAversion: a.Traits.LossAversion,
		},
		Holdings:   hold,
		Delegation: n.delegationOf(a.ID),
		Adversary:  adv,
		Market:     n.desk.holding(a.ID),
		Owner:      n.money.owner(a),
	}
}

func (n *Idents) delegationOf(id population.ActorID) store.Delegation {
	s, ok := n.chain.View().StakeOf(id)
	if !ok {
		return store.Delegation{}
	}

	var pool, drep string
	var actor uint64
	if s.Delegated {
		pool = bech32Of("pool", s.Pool[:])
		if pa, ok := n.chain.View().PoolActor(s.Pool); ok {
			actor = uint64(pa)
		}
	}
	if s.Votes {
		drep = drepText(s.DRep)
	}

	return store.Delegation{
		StakeRegistered: s.Registered,
		DelegatedPool:   pool,
		DelegatedActor:  actor,
		DelegatedDRep:   drep,
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
