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
//
// Engine is a composite: each concern is a component under internal/, which
// owns its state and its part of the snapshot (Save and Load). No component
// imports another; they share only the value types in wire, and reach each
// other through interfaces they declare, which the engine satisfies with
// small adapters. Engine orders every call, so the order of journal
// records and RNG draws is decided here, in Handle and the steps it runs.
//
//	```mermaid
//	flowchart TD
//	  cmd[cmd/continuum, httpapi, sink] --> engine
//	  engine[engine: Engine composite, Runner, aliases] --> clock & journal & window & cohort & pace & pay & intent & floor & steer & faucet & judge & attack & meter & live & fakenode
//	  journal & window & cohort & pace & pay & intent & floor & steer & faucet & judge & attack & meter & live & fakenode --> wire
//	  clock --> sched[internal/sched]
//	  window --> chain[internal/chain]
//	  pay --> plan[internal/plan] & submit[internal/submit]
//	  floor --> policy[internal/policy]
//	  judge --> oracle[internal/oracle] & mistake[internal/mistake]
//	  attack --> adversary[internal/adversary]
//	  intent --> build[internal/build]
//	  cohort --> population[internal/population] & keys[internal/keys]
//	  wire --> eventlog[internal/eventlog] & sched
//	```
//
// internal/archtest enforces the component boundary.
package engine

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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
// engines with equal configs fed equal inputs make equal decisions. Its
// fields live in embedded groups, so a read is still cfg.K; only a
// composite literal names the group.
type Config struct {
	Seeded
	Chain
	Submit
	Funding
	Guard
	World
}

// Seeded is what names a run: its seed, profiles and genesis cohorts.
type Seeded struct {
	Seed     rng.Seed
	Profiles *profile.Registry
	// Fitted, when set, is Profiles before a rate scale: the header's profile
	// hash covers it instead of Profiles, so the hash names the profile file
	// whatever the scale, and Scale carries the factor into the genesis
	// fingerprint. Zero Scale means 1.
	Fitted  *profile.Registry
	Scale   float64
	Cohorts []Cohort
	// BinaryVersion goes into the log header.
	BinaryVersion string
}

// Chain is the shape of the chain the run lives on.
type Chain struct {
	Slots sched.Slots
	// K is the security parameter: a block is durable once K blocks follow it.
	K uint64
	// BlockEvery is the fake chain's block interval in slots. It is part of
	// the genesis fingerprint, so a log is replayed only against the chain
	// shape it was recorded on.
	BlockEvery uint64
	NetworkID  uint8
	Build      build.Params
	// ChainParams are a fake chain's protocol parameters (NewFakeChain);
	// nil is fakechain.DefaultParams. Build must be taken from the same
	// ones.
	ChainParams *conway.ConwayProtocolParameters
	// TTL is how many slots after its decision a payment stays valid.
	TTL uint64
	// NOpt is the protocol parameter nOpt, the view's saturation cap; zero
	// means none is known. GovLifetime is govActionLifetime in epochs.
	// Both come from the node's (or the fake chain's) parameters.
	NOpt, GovLifetime uint64
	// Horizon is the stability window 3k/f in slots: a node translates a
	// script transaction's validity interval to time only within it, and
	// answers PastHorizon beyond. A script attempt's TTL stays inside it;
	// zero means no bound.
	Horizon uint64
	// HardFork is the hard fork the network proposer proposes by itself
	// (internal/engine/internal/steer ForkStep); the zero value schedules none. It is part of the log
	// header.
	HardFork protocol.Schedule
}

// Submit is how payments are chosen, retried and judged.
type Submit struct {
	// Selector chooses payment inputs; nil means LargestFirst.
	Selector Selector
	// Retry shapes the waits between attempts at a payment; the zero value
	// means submit.DefaultPolicy. It is part of the log header.
	Retry submit.Policy
	// Diagnoser reads a node's rejection for the retry machine; nil means
	// submit.Perfect. It chooses the fix and never the refusal.
	Diagnoser submit.Diagnoser
	// Mistakes makes actors err (internal/mistake): each intent samples
	// one honest mistake from the actor's competence and care, every
	// attempt carries it until a fix removes it, and a rejection is read
	// by an actor of that competence. Off, every attempt is as built.
	Mistakes bool
	// Run names the run in each oracle finding's replay command. Findings
	// are decisions, so a replay must use the same Run.
	Run oracle.Run
}

// Funding is how the genesis is funded and how the pools and committee
// start.
type Funding struct {
	// Faucet, when positive, is the lovelace the run's faucet holds at
	// genesis, and the population starts with nothing: the faucet refills
	// it. The faucet is keys.Store.Faucet of the root Seed, the wallet
	// internal/genesis funds, so the engine spends a devnet's genesis funds
	// with no key import. Zero funds every genesis actor directly.
	Faucet uint64
	// RefillEvery is the slots between the faucet's refill rounds. It must
	// be positive when Faucet is.
	RefillEvery uint64
	// GenesisPools is the number of pools the genesis registers
	// (internal/genesis Spec.Pools), each owner holding GenesisPoolStake
	// delegated to it. The first GenesisPools pool actors operate them.
	GenesisPools     int
	GenesisPoolStake uint64
	// Committee is the number of genesis constitutional committee members,
	// the run seed's keystore.Committee(0..Committee-1).
	Committee int
}

// Guard is the stake-floor guard and the pods behind the pools.
type Guard struct {
	// Floor is the stake-floor guard's configuration; the zero value is a
	// 50% floor with a margin of one pool.
	Floor policy.Config
	// SimulatedPods: no operator runs the pools (the fake chain), so every
	// pod runs unless a PoolStatus input says otherwise, and a repair
	// brings it back at once.
	SimulatedPods bool
}

// World is a fitted file's non-wallet populations and chain-wide rates, as
// Populate set them, with the clearance for adversaries. The log header's
// profile hash covers the populations and rates.
type World struct {
	Populations []profile.Population
	Network     profile.Network
	// Markets is the ADA market the wallets trade in (internal/market);
	// nil means none. Populate writes an actor for each exchange.
	Markets *profile.Markets
	// Adversaries is the proof (adversary.Clear) that the node is on the
	// run's devnet. Without it New refuses any adversary actor.
	Adversaries adversary.Clearance
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

func (c *Submit) retry() submit.Policy {
	if c.Retry == (submit.Policy{}) {
		return submit.DefaultPolicy
	}
	return c.Retry
}

// hardFork is the schedule as the header names it, empty without one so a
// log written before schedules existed keeps its genesis hash.
func (c Config) hardFork() string {
	if !c.HardFork.On() {
		return ""
	}
	return c.HardFork.String()
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
	if at := c.Build.Protocol; c.HardFork.On() && at != (protocol.Version{}) && !protocol.CanFollow(c.HardFork.To, at) {
		return fmt.Errorf("%w: hard fork to %v cannot follow the genesis version %v", ErrConfig, c.HardFork.To, at)
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

// ProfileHash is the fingerprint of a profile set: SHA-256 of its canonical
// marshalled form, profiles in name order.
func ProfileHash(reg *profile.Registry) ([32]byte, error) {
	return FileHash(profile.File{Profiles: profilesIn(reg)})
}

// FileHash is the fingerprint of a whole profile file: SHA-256 of its
// canonical form (profile.MarshalFile), profiles in name order. A file
// with no populations and no network rates hashes as ProfileHash of its
// profiles, so logs and targets of such files keep their hash.
func FileHash(f profile.File) ([32]byte, error) {
	f.Profiles = slices.Clone(f.Profiles)
	slices.SortFunc(f.Profiles, func(a, b profile.Profile) int { return strings.Compare(a.Name, b.Name) })
	raw, err := profile.MarshalFile(f)
	if err != nil {
		return [32]byte{}, fmt.Errorf("engine: marshal profiles: %w", err)
	}
	return sha256.Sum256(raw), nil
}

// fileHash is the header's profile hash: the profiles before any rate
// scale, less the ones Populate wrote, with the populations and network
// rates they came from.
func (c *Config) fileHash() ([32]byte, error) {
	reg := c.Profiles
	if c.Fitted != nil {
		reg = c.Fitted
	}
	var ps []profile.Profile
	for _, p := range profilesIn(reg) {
		if _, written := c.population(p.Name); written || (p.Name == proposerName && len(c.Network.Proposals) > 0) || c.exchange(p.Name) {
			continue
		}
		ps = append(ps, p)
	}
	return FileHash(profile.File{Profiles: ps, Populations: c.Populations, Network: c.Network, Markets: c.Markets})
}

// Header returns the log header for this config: fingerprints of the seed,
// the profile set and the genesis, never the seed itself.
func (c *Config) Header() (eventlog.HeaderBody, error) {
	funds, err := c.Genesis()
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	ph, err := c.fileHash()
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	type fund struct {
		Actor    uint64 `json:"actor"`
		Address  []byte `json:"address"`
		Lovelace uint64 `json:"lovelace"`
	}
	g := struct {
		Funds      []fund        `json:"funds"`
		K          uint64        `json:"k"`
		BlockEvery uint64        `json:"block_every"`
		NetworkID  uint8         `json:"network_id"`
		SlotMs     sched.Time    `json:"slot_ms"`
		EpochMs    sched.Time    `json:"epoch_ms"`
		TTL        uint64        `json:"ttl"`
		Build      params        `json:"build"`
		Format     uint32        `json:"format"`
		Retry      submit.Policy `json:"retry"`
		Refill     uint64        `json:"refill,omitzero"`
		NOpt       uint64        `json:"n_opt,omitzero"`
		GovLife    uint64        `json:"gov_life,omitzero"`
		Floor      uint64        `json:"floor"`
		Margin     int           `json:"margin"`
		SimPods    bool          `json:"sim_pods,omitzero"`
		GPools     int           `json:"g_pools,omitzero"`
		GStake     uint64        `json:"g_stake,omitzero"`
		Scale      float64       `json:"scale,omitzero"`
		Committee  int           `json:"committee,omitzero"`
		Mistakes   bool          `json:"mistakes,omitzero"`
		Horizon    uint64        `json:"horizon,omitzero"`
		HardFork   string        `json:"hard_fork,omitempty"`
	}{HardFork: c.hardFork(), Mistakes: c.Mistakes, Horizon: c.Horizon, Committee: c.Committee, GPools: c.GenesisPools, GStake: c.GenesisPoolStake, NOpt: c.NOpt, GovLife: c.GovLifetime, Floor: c.floor().Floor, Margin: c.floor().Margin, SimPods: c.SimulatedPods, Format: logFormat, K: c.K, BlockEvery: c.BlockEvery, NetworkID: c.NetworkID, SlotMs: c.Slots.SlotStart(1),
		EpochMs: c.Slots.EpochStart(1), TTL: c.TTL, Build: paramsOf(c.Build), Retry: c.retry(), Refill: c.RefillEvery}
	if c.Scale != 0 && c.Scale != 1 {
		g.Scale = c.Scale
	}
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
	gh, sh := sha256.Sum256(graw), sha256.Sum256(fp[:])
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
const faucetActor = cohort.Faucet

// genesisPools lists the genesis pools and their operators. root is the
// keystore of the run seed itself.
func (c *Config) genesisPools(root *keys.Store) ([]cohort.GenesisPool, error) {
	groups := make([]cohort.Group, len(c.Cohorts))
	for i, co := range c.Cohorts {
		p, _ := c.Profiles.Get(co.Profile)
		groups[i] = cohort.Group{Kind: p.Kind, Count: co.Count}
	}
	out, err := cohort.GenesisPools(c.GenesisPools, groups, root)
	if errors.As(err, &cohort.ShortError{}) {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return out, err
}

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

// defaultFloor fills the fields of Config.Floor left zero.
var defaultFloor = policy.Config{Floor: 50, Margin: 1}

func (c *Guard) floor() policy.Config {
	f := c.Floor
	if f.Floor == 0 {
		f.Floor = defaultFloor.Floor
	}
	if f.Margin == 0 {
		f.Margin = defaultFloor.Margin
	}
	return f
}

func (c *Config) spawns(name string, p profile.Profile) bool {
	if p.Hazards.Birth > 0 {
		return true
	}
	return slices.ContainsFunc(c.Cohorts, func(co Cohort) bool { return co.Profile == name && co.Count > 0 })
}

// checkAdversaries refuses an adversary profile that is malformed, and one
// the run would spawn without a devnet clearance.
func (c *Config) checkAdversaries() error {
	for _, n := range c.Profiles.Names() {
		p, _ := c.Profiles.Get(n)
		if p.Adversary == nil {
			continue
		}
		pol, err := adversary.NewPolicy(*p.Adversary)
		if err == nil {
			_, err = agenda.WithAttacks(p.Agenda, pol)
		}
		if err != nil {
			return fmt.Errorf("%w: profile %q: %w", ErrConfig, n, err)
		}
		if c.spawns(n, p) && !c.Adversaries.Cleared() {
			return fmt.Errorf("engine: profile %q: %w", n, adversary.ErrNotDevnet)
		}
	}
	return nil
}

// population is the population called name.
func (c *World) population(name string) (profile.Population, bool) {
	for _, p := range c.Populations {
		if p.Name == name {
			return p, true
		}
	}
	return profile.Population{}, false
}

// exchange reports whether the named profile is an exchange's actor, which
// Populate writes from the markets section.
func (c *World) exchange(name string) bool {
	if c.Markets == nil {
		return false
	}
	for _, x := range c.Markets.Exchanges {
		if market.ProfileName(x.Name) == name {
			return true
		}
	}
	return false
}

// contractParams is the file's contract population, if it has one.
func (c *World) contractParams() (*profile.ContractParams, bool) {
	for _, p := range c.Populations {
		if p.Contract != nil {
			return p.Contract, true
		}
	}
	return nil, false
}

// GenesisPoolActors maps each genesis pool's index to the pool actor that
// operates it, so a live run can find the pool's ContinuumPool.
func (c *Config) GenesisPoolActors() (map[int]uint64, error) {
	root, err := keys.New(c.Seed, c.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	gs, err := c.genesisPools(root)
	if err != nil {
		return nil, err
	}
	out := map[int]uint64{}
	for _, g := range gs {
		out[g.Index] = uint64(g.Actor)
	}
	return out, nil
}

// params is build.Params as the fingerprint encodes it. The gouroboros
// ExUnits and ExUnitPrice embed cbor.StructAsArray, which JSON cannot
// encode, so these fields shadow Params' own as plain values: units as
// (memory, steps), prices as "num/den" rationals.
type params struct {
	// Params is every other parameter, encoded as it is.
	build.Params
	// Prices are the memory and step prices.
	Prices [2]string `json:"Prices"`
	// MaxTxExUnits are the per-transaction memory and steps.
	MaxTxExUnits [2]int64 `json:"MaxTxExUnits"`
	// MaxBlockExUnits are the per-block memory and steps.
	MaxBlockExUnits [2]int64 `json:"MaxBlockExUnits"`
}

func paramsOf(p build.Params) params {
	return params{
		Params:          p,
		Prices:          [2]string{ratio(p.Prices.MemPrice), ratio(p.Prices.StepPrice)},
		MaxTxExUnits:    [2]int64{p.MaxTxExUnits.Memory, p.MaxTxExUnits.Steps},
		MaxBlockExUnits: [2]int64{p.MaxBlockExUnits.Memory, p.MaxBlockExUnits.Steps},
	}
}

func ratio(r *cbor.Rat) string {
	if r == nil || r.Rat == nil {
		return ""
	}
	return r.RatString()
}
