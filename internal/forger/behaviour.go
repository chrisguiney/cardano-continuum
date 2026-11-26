package forger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// Kind names a behaviour.
type Kind string

// Behaviours. Each misbehaviour breaks one rule and nothing else, so a
// node's reaction can be read as its handling of that rule.
const (
	// Honest forges the block an honest pool would.
	Honest Kind = "honest"
	// Equivocate forges two valid blocks for one slot and offers them to
	// disjoint peer sets.
	Equivocate Kind = "equivocate"
	// OpCertRewind uses a certificate whose counter is below the pool's
	// counter on chain.
	OpCertRewind Kind = "opcert-rewind"
	// OpCertLeap uses a certificate whose counter is more than one above
	// the pool's counter on chain.
	OpCertLeap Kind = "opcert-leap"
	// KESBeforeStart uses a certificate whose KES period has not begun.
	KESBeforeStart Kind = "kes-before-start"
	// KESExpired forges with a certificate past its last KES evolution. It
	// applies only once the chain is that old.
	KESExpired Kind = "kes-expired"
	// KESWrongPeriod signs with the KES key at another evolution than the
	// slot's.
	KESWrongPeriod Kind = "kes-wrong-period"
	// VRFBadProof carries a leader proof made for another slot.
	VRFBadProof Kind = "vrf-bad-proof"
	// VRFNotLeader forges in a slot the pool does not lead.
	VRFNotLeader Kind = "vrf-not-leader"
	// BodyHash serves a body the header does not commit to.
	BodyHash Kind = "body-hash"
	// Oversize forges a body over the maximum block body size.
	Oversize Kind = "oversize-body"
	// FutureSlot forges, now, a block for a slot the pool leads later in
	// the epoch.
	FutureSlot Kind = "future-slot"
)

// futureGap is how many slots ahead of the clock a FutureSlot block is at
// least: far past the clock skew a node tolerates (cardano-node's default
// is 2 s).
const futureGap = 30

// Kinds is every misbehaviour, in a fixed order.
func Kinds() []Kind {
	return []Kind{Equivocate, OpCertRewind, OpCertLeap, KESBeforeStart, KESExpired, KESWrongPeriod,
		VRFBadProof, VRFNotLeader, BodyHash, Oversize, FutureSlot}
}

// Rule enables one misbehaviour: at each slot where it applies it fires
// with probability Rate.
type Rule struct {
	Kind Kind    `json:"kind"`
	Rate float64 `json:"rate"`
}

// Schedule is a scenario's misbehaviours. With no rules the forger is
// honest. Every decision is drawn from (Seed, kind, slot) alone, so a run
// is reproduced by its seed whatever the order slots were visited in.
type Schedule struct {
	Seed  rng.Seed
	Rules []Rule
}

type scheduleDoc struct {
	Seed  string `json:"seed"`
	Rules []Rule `json:"rules"`
}

// MarshalJSON writes the seed as hex.
func (s Schedule) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduleDoc{Seed: hex.EncodeToString(s.Seed[:]), Rules: s.Rules})
}

// UnmarshalJSON reads what MarshalJSON writes.
func (s *Schedule) UnmarshalJSON(b []byte) error {
	var d scheduleDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return err
	}
	raw, err := hex.DecodeString(d.Seed)
	if err != nil || len(raw) != len(s.Seed) {
		return fmt.Errorf("schedule seed must be %d hex bytes", len(s.Seed))
	}
	copy(s.Seed[:], raw)
	s.Rules = d.Rules
	return nil
}

func (s Schedule) check() error {
	known := Kinds()
	for _, r := range s.Rules {
		if !slices.Contains(known, r.Kind) {
			return fmt.Errorf("unknown misbehaviour %q", r.Kind)
		}
		if !(r.Rate >= 0 && r.Rate <= 1) {
			return fmt.Errorf("misbehaviour %s: rate %v is not a probability", r.Kind, r.Rate)
		}
	}
	return nil
}

// State is what a slot's plan depends on.
type State struct {
	Slot uint64
	// Leader is whether the pool leads Slot.
	Leader bool
	// Tip is the forger's selected chain tip, the parent of a new block.
	Tip Tip
	// Nonce is the nonce of Slot's epoch.
	Nonce [32]byte
	// Counter is the pool's highest operational certificate counter on
	// chain; Forged is false while the pool has no block on chain.
	Counter uint64
	Forged  bool
	// Peers are the connected peers, sorted.
	Peers []PeerID
}

// Planner turns a slot's state into the blocks to forge. It is pure.
type Planner struct {
	p Params
	c *Credentials
	s Schedule
}

// NewPlanner checks s against the misbehaviours this build knows.
func NewPlanner(p Params, c *Credentials, s Schedule) (*Planner, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, errors.New("planner needs credentials")
	}
	if err := s.check(); err != nil {
		return nil, err
	}
	return &Planner{p: p, c: c, s: s}, nil
}

// Plan is the blocks to forge at st.Slot: those of the first rule that
// fires and applies, else an honest block when the pool leads, else none.
func (pl *Planner) Plan(st State) ([]Forge, error) {
	for _, r := range pl.s.Rules {
		if !pl.fires(r, st.Slot) {
			continue
		}
		fs, err := pl.misbehave(r.Kind, st)
		if err != nil {
			return nil, err
		}
		if len(fs) > 0 {
			return fs, nil
		}
	}
	if !st.Leader {
		return nil, nil
	}
	return []Forge{pl.base(Honest, st)}, nil
}

func (pl *Planner) fires(r Rule, slot uint64) bool {
	return pl.draw(r.Kind, slot).Rand().Float64() < r.Rate
}

func (pl *Planner) draw(k Kind, slot uint64) rng.Seed {
	return pl.s.Seed.Derive("forger", string(k), strconv.FormatUint(slot, 10))
}

// base is the honest block at st.
func (pl *Planner) base(k Kind, st State) Forge {
	return Forge{Kind: k, Parent: st.Tip, Slot: st.Slot, VRFSlot: st.Slot, Expect: Adopt}
}

func (pl *Planner) invalid(k Kind, st State, rule string) Forge {
	f := pl.base(k, st)
	f.Expect, f.Rule = Reject, rule
	return f
}

// misbehave plans kind at st, or nothing when it does not apply there.
func (pl *Planner) misbehave(k Kind, st State) ([]Forge, error) {
	switch k {
	case VRFNotLeader:
		if st.Leader {
			return nil, nil
		}
		return []Forge{pl.invalid(k, st, "VRFLeaderValueTooBig")}, nil
	case FutureSlot:
		return pl.future(st)
	}
	if !st.Leader {
		return nil, nil
	}
	switch k {
	case Equivocate:
		return pl.equivocate(st), nil
	case OpCertRewind, OpCertLeap, KESBeforeStart, KESExpired:
		return pl.withCert(k, st), nil
	}
	f := pl.invalid(k, st, "")
	switch k {
	case KESWrongPeriod:
		f.KESShift, f.Rule = 1, "InvalidKesSignatureOCERT"
	case VRFBadProof:
		f.VRFSlot, f.Rule = st.Slot+1, "VRFKeyBadProof"
	case BodyHash:
		f.BadBodyHash, f.Rule = true, "InvalidBodyHashBBODY"
	case Oversize:
		f.Pad, f.Rule = pl.p.MaxBodySize+1, "BlockSizeTooLarge"
	default:
		return nil, fmt.Errorf("misbehaviour %q has no plan", k)
	}
	return []Forge{f}, nil
}

// withCert plans the misbehaviours that are a choice of certificate. Each
// applies only where its certificate breaks the rule: a rewind certificate
// is valid until the chain has seen a higher counter.
func (pl *Planner) withCert(k Kind, st State) []Forge {
	period := st.Slot / pl.p.SlotsPerKESPeriod
	var name CertName
	var rule string
	var applies func(OpCert) bool
	switch k {
	case OpCertRewind:
		name, rule = CertRewind, "CounterTooSmallOCERT"
		applies = func(c OpCert) bool { return st.Forged && c.Counter < st.Counter }
	case OpCertLeap:
		name, rule = CertLeap, "CounterOverIncrementedOCERT"
		applies = func(c OpCert) bool { return c.Counter > st.Counter+1 }
	case KESBeforeStart:
		name, rule = CertFuture, "KESBeforeStartOCERT"
		applies = func(c OpCert) bool { return period < c.Start }
	case KESExpired:
		name, rule = CertPrimary, "KESAfterEndOCERT"
		applies = func(c OpCert) bool { return period >= c.Start+pl.p.MaxKESEvolutions }
	}
	cert, ok := pl.c.Certs[name]
	if !ok || !applies(cert) {
		return nil
	}
	f := pl.invalid(k, st, rule)
	f.Cert = name
	return []Forge{f}
}

// equivocate plans two valid blocks for the slot, each for half of the
// peers. With fewer than two peers there is nobody to split, and the plan
// is an honest block.
func (pl *Planner) equivocate(st State) []Forge {
	if len(st.Peers) < 2 {
		return nil
	}
	peers := slices.Clone(st.Peers)
	slices.Sort(peers)
	r := pl.draw(Equivocate, st.Slot).Derive("split").Rand()
	r.Shuffle(len(peers), func(i, j int) { peers[i], peers[j] = peers[j], peers[i] })
	half := (len(peers) + 1) / 2
	a, b := pl.base(Equivocate, st), pl.base(Equivocate, st)
	a.SendTo, b.SendTo = sorted(peers[:half]), sorted(peers[half:])
	b.MinorBump = 1
	return []Forge{a, b}
}

func sorted(ps []PeerID) []PeerID {
	out := slices.Clone(ps)
	slices.Sort(out)
	return out
}

// future plans a block for the first slot the pool leads at least
// futureGap slots from now, in this epoch: a later epoch's nonce is not
// known yet. The block is valid when its slot comes, and must not be
// accepted before.
func (pl *Planner) future(st State) ([]Forge, error) {
	end := (pl.p.Epoch(st.Slot) + 1) * pl.p.EpochLength
	for s := st.Slot + futureGap; s < end; s++ {
		lead, err := Leader(pl.p, pl.c, st.Nonce, s)
		if err != nil {
			return nil, err
		}
		if !lead {
			continue
		}
		f := pl.invalid(FutureSlot, st, "header slot ahead of the wall clock beyond the clock skew")
		f.Slot, f.VRFSlot = s, s
		return []Forge{f}, nil
	}
	return nil, nil
}
