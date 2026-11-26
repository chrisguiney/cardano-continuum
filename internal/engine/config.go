// Package engine assembles the simulation: the population, its agendas, the
// engine's own view of the chain and payment building, behind one pure step
// (Engine.Handle) that the live loop (Runner) and replay both drive.
//
// Durable actor state comes only from blocks K deep (design A of the
// engine-recovery model): a block enters a volatile window of the last K
// blocks and changes balances only when its Kth successor arrives, so a
// rollback the node can make never has to undo durable state. The window
// itself is in the snapshot, so a restart can unwind it when FindIntersect
// answers below the stored tip.
//
// Outputs, reservations and when a payment is settled or dead belong to the
// planner's ledger (internal/plan), which Handle feeds the same blocks and
// rollbacks. The ledger lives inside the pure state: its changes are
// reproduced by replaying the inputs that caused them, and its blob rides in
// the snapshot and in the checkpoint's Reservations.
//
// Everything Handle decides is a function of the configuration, the seed and
// the logged inputs: it reads no clock (time arrives as Tick inputs), starts
// no goroutine and never lets map order reach an output.
package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Cohort is a group of actors of one profile that exists at genesis, each
// funded with the profile's budget.
type Cohort struct {
	Profile string
	Count   int
}

// Config is everything an engine is built from besides its inputs. Two
// engines with equal configs fed equal inputs make equal decisions.
type Config struct {
	Seed     rng.Seed
	Profiles *profile.Registry
	Cohorts  []Cohort
	Slots    sched.Slots
	// K is the security parameter: a block is durable once K blocks follow it.
	K uint64
	// BlockEvery is the fake chain's block interval in slots. It is part of
	// the genesis fingerprint, so a log is replayed only against the chain
	// shape it was recorded on.
	BlockEvery uint64
	NetworkID  uint8
	Build      build.Params
	// TTL is how many slots after its decision a payment stays valid.
	TTL uint64
	// Selector chooses payment inputs; nil means LargestFirst.
	Selector Selector
	// BinaryVersion goes into the log header.
	BinaryVersion string

	// Retry shapes the waits between attempts at a payment; the zero value
	// means submit.DefaultPolicy. It is part of the log header.
	Retry submit.Policy
	// Diagnoser reads a node's rejection for the retry machine; nil means
	// submit.Perfect. It chooses the fix and never the refusal.
	Diagnoser submit.Diagnoser

	// Faucet, when positive, is the lovelace the run's faucet holds at
	// genesis, and the population starts with nothing: the faucet refills
	// it. The faucet is keys.Store.Faucet of the root Seed, the wallet
	// internal/genesis funds, so the engine spends a devnet's genesis funds
	// with no key import. Zero funds every genesis actor directly.
	Faucet uint64
	// RefillEvery is the slots between the faucet's refill rounds. It must
	// be positive when Faucet is.
	RefillEvery uint64

	// NOpt is the protocol parameter nOpt, the view's saturation cap; zero
	// means none is known. GovLifetime is govActionLifetime in epochs.
	// Both come from the node's (or the fake chain's) parameters.
	NOpt, GovLifetime uint64

	// Floor is the stake-floor guard's configuration; the zero value is a
	// 50% floor with a margin of one pool.
	Floor policy.Config
	// SimulatedPods: no operator runs the pools (the fake chain), so every
	// pod runs unless a PoolStatus input says otherwise, and a repair
	// brings it back at once.
	SimulatedPods bool

	// GenesisPools is the number of pools the genesis registers
	// (internal/genesis Spec.Pools), each owner holding GenesisPoolStake
	// delegated to it. The first GenesisPools pool actors operate them.
	GenesisPools     int
	GenesisPoolStake uint64
}

// Fund is one genesis output.
type Fund struct {
	Actor    uint64
	Address  common.Address
	Lovelace uint64
}

// Errors returned for configurations the engine cannot run.
var (
	ErrConfig = errors.New("engine: invalid config")
)

// maxActors keeps every actor ID a valid CIP-1852 account index.
const maxActors = 1 << 30

func (c *Config) retry() submit.Policy {
	if c.Retry == (submit.Policy{}) {
		return submit.DefaultPolicy
	}
	return c.Retry
}

func (c *Config) validate() error {
	if c.Profiles == nil {
		return fmt.Errorf("%w: no profiles", ErrConfig)
	}
	if c.K == 0 {
		return fmt.Errorf("%w: K must be positive", ErrConfig)
	}
	if c.TTL == 0 {
		return fmt.Errorf("%w: TTL must be positive", ErrConfig)
	}
	total := 0
	for _, co := range c.Cohorts {
		if _, ok := c.Profiles.Get(co.Profile); !ok {
			return fmt.Errorf("%w: cohort names unknown profile %q", ErrConfig, co.Profile)
		}
		if co.Count < 0 {
			return fmt.Errorf("%w: cohort %q count %d", ErrConfig, co.Profile, co.Count)
		}
		total += co.Count
	}
	for _, n := range c.Profiles.Names() {
		p, _ := c.Profiles.Get(n)
		if p.Kind != profile.Wallet && p.Kind != profile.Pool && p.Kind != profile.DRep {
			// A contract is deployed by another actor's lock; it is not
			// an actor of its own here.
			return fmt.Errorf("%w: profile %q has kind %s; wallets, pools and DReps are supported", ErrConfig, n, p.Kind)
		}
	}
	if c.Faucet > 0 && c.RefillEvery == 0 {
		return fmt.Errorf("%w: a faucet needs RefillEvery", ErrConfig)
	}
	if total > maxActors {
		return fmt.Errorf("%w: %d actors over %d", ErrConfig, total, maxActors)
	}
	return nil
}

// Genesis returns the genesis outputs: actors 1..n in cohort order, each
// funded with its profile's budget, or with a Faucet only the faucet's
// output. Actors born later start with nothing.
func (c *Config) Genesis() ([]Fund, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.Faucet > 0 {
		w, err := faucetWallet(c)
		if err != nil {
			return nil, err
		}
		return []Fund{{Actor: uint64(faucetActor), Address: w.Address(), Lovelace: c.Faucet}}, nil
	}
	ks, err := keys.New(c.Seed.Derive("keys"), c.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: keystore: %w", err)
	}
	var out []Fund
	id := uint64(0)
	for _, co := range c.Cohorts {
		p, _ := c.Profiles.Get(co.Profile)
		for range co.Count {
			id++
			out = append(out, Fund{Actor: id, Address: ks.Wallet(keys.ActorID(id)).Address(), Lovelace: uint64(p.Budget)})
		}
	}
	return out, nil
}

// Header returns the log header for this config: fingerprints of the seed,
// the profile set and the genesis, never the seed itself.
func (c *Config) Header() (eventlog.HeaderBody, error) {
	funds, err := c.Genesis()
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	var ps []profile.Profile
	for _, n := range c.Profiles.Names() {
		p, _ := c.Profiles.Get(n)
		ps = append(ps, p)
	}
	praw, err := profile.Marshal(ps)
	if err != nil {
		return eventlog.HeaderBody{}, fmt.Errorf("engine: marshal profiles: %w", err)
	}
	type fund struct {
		Actor    uint64 `cbor:"1,keyasint"`
		Address  []byte `cbor:"2,keyasint"`
		Lovelace uint64 `cbor:"3,keyasint"`
	}
	g := struct {
		Funds      []fund        `cbor:"1,keyasint"`
		K          uint64        `cbor:"2,keyasint"`
		BlockEvery uint64        `cbor:"3,keyasint"`
		NetworkID  uint8         `cbor:"4,keyasint"`
		SlotMs     sched.Time    `cbor:"5,keyasint"`
		EpochMs    sched.Time    `cbor:"6,keyasint"`
		TTL        uint64        `cbor:"7,keyasint"`
		Build      build.Params  `cbor:"8,keyasint"`
		Format     uint32        `cbor:"9,keyasint"`
		Retry      submit.Policy `cbor:"10,keyasint"`
		Refill     uint64        `cbor:"11,keyasint,omitempty"`
		NOpt       uint64        `cbor:"12,keyasint,omitempty"`
		GovLife    uint64        `cbor:"13,keyasint,omitempty"`
		Floor      uint64        `cbor:"14,keyasint"`
		Margin     int           `cbor:"15,keyasint"`
		SimPods    bool          `cbor:"16,keyasint,omitempty"`
		GPools     int           `cbor:"17,keyasint,omitempty"`
		GStake     uint64        `cbor:"18,keyasint,omitempty"`
	}{GPools: c.GenesisPools, GStake: c.GenesisPoolStake, NOpt: c.NOpt, GovLife: c.GovLifetime, Floor: c.floor().Floor, Margin: c.floor().Margin, SimPods: c.SimulatedPods, Format: logFormat, K: c.K, BlockEvery: c.BlockEvery, NetworkID: c.NetworkID, SlotMs: c.Slots.SlotStart(1),
		EpochMs: c.Slots.EpochStart(1), TTL: c.TTL, Build: c.Build, Retry: c.retry(), Refill: c.RefillEvery}
	for _, f := range funds {
		a, err := f.Address.Bytes()
		if err != nil {
			return eventlog.HeaderBody{}, fmt.Errorf("engine: genesis address: %w", err)
		}
		g.Funds = append(g.Funds, fund{f.Actor, a, f.Lovelace})
	}
	graw, err := eventlog.Encode(g)
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	fp := c.Seed.Derive("fingerprint")
	ph, gh, sh := sha256.Sum256(praw), sha256.Sum256(graw), sha256.Sum256(fp[:])
	return eventlog.HeaderBody{
		SeedFingerprint: sh[:],
		ProfileHash:     ph[:],
		GenesisHash:     gh[:],
		BinaryVersion:   c.BinaryVersion,
	}, nil
}

// Factory returns the eventlog factory for this config: a fresh engine from
// nil, or one restored from a snapshot.
func (c Config) Factory() eventlog.Factory {
	return func(snap []byte) (eventlog.Engine, error) {
		if snap == nil {
			return New(c)
		}
		return Restore(c, snap)
	}
}

// faucetActor is the faucet's owner ID in the ledger. It is above maxActors,
// so no census actor can take it.
const faucetActor = population.ActorID(keys.FaucetID)

// faucetWallet is internal/genesis's faucet for c's seed: keys.New on the
// root seed itself, where actor keys come from a derived one.
func faucetWallet(c *Config) (keys.Wallet, error) {
	ks, err := keys.New(c.Seed, c.NetworkID)
	if err != nil {
		return keys.Wallet{}, fmt.Errorf("engine: faucet keystore: %w", err)
	}
	return ks.Faucet(), nil
}

// SeedFromInt turns a small integer into a root seed, for reproducible fake
// runs (`--seed N`). Real runs use a random seed from the store.
func SeedFromInt(n uint64) rng.Seed {
	var s rng.Seed
	copy(s[:], "continuum/seed/int")
	binary.BigEndian.PutUint64(s[24:], n)
	return s.Derive("int-seed")
}
