// Package simflags holds the flags that fix a simulation's configuration,
// which run and replay share and live run builds on.
package simflags

import (
	"flag"
	"strconv"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// Version goes into the log header; a log replays only under the binary
// version that wrote it.
const Version = "m1"

// Flags are the flags that fix a run's configuration, in three groups: what
// the run is (Run), who takes part (Population) and the chain they act on
// (Chain).
type Flags struct {
	Run
	Population
	Chain
}

// Register adds every flag to fs.
func (f *Flags) Register(fs *flag.FlagSet) {
	f.Run.register(fs)
	f.Population.register(fs)
	f.Chain.register(fs)
}

// Config builds the engine configuration of a fake-chain run.
func (f *Flags) Config() (engine.Config, error) {
	fitted, cohorts, file, err := f.Load()
	if err != nil {
		return engine.Config{}, err
	}
	reg, err := f.Scaled(fitted)
	if err != nil {
		return engine.Config{}, err
	}
	slots, err := f.Slots()
	if err != nil {
		return engine.Config{}, err
	}
	mistakes, err := f.MistakesOn(fitted)
	if err != nil {
		return engine.Config{}, err
	}
	pp, err := f.ChainParams()
	if err != nil {
		return engine.Config{}, err
	}
	bp := build.ParamsFromConway(&pp, 0)
	// The fake chain's slot 0 is the Unix epoch.
	bp.SystemStart, bp.SlotLength = time.UnixMilli(0).UTC(), time.Duration(f.SlotMs)*time.Millisecond
	fork, err := f.HardForkSchedule(bp.Protocol)
	if err != nil {
		return engine.Config{}, err
	}
	cfg := engine.Config{
		Seed: engine.SeedFromInt(f.Seed), Profiles: reg, Fitted: fitted, Scale: f.Scale, Cohorts: cohorts, Slots: slots,
		K: f.K, BlockEvery: f.BlockEvery, TTL: f.TTL, BinaryVersion: Version, Build: bp, ChainParams: &pp,
		NOpt: uint64(pp.NOpt), GovLifetime: pp.GovActionValidityPeriod, SimulatedPods: true,
		Floor: policy.Config{Floor: f.Floor, Margin: 1},
		// The fake chain's genesis registers a pool for every operator and
		// seats internal/genesis's committee, as a devnet's does.
		GenesisPools: f.Pools, GenesisPoolStake: genesis.Default().PoolStake, Committee: genesis.Default().Committee,
		Mistakes: mistakes, HardFork: fork, Run: f.Oracle(mistakes, fork),
	}
	if err := engine.Populate(&cfg, file); err != nil {
		return engine.Config{}, err
	}
	// A fitted file's pools are genesis pools of the fake chain, as a
	// devnet's pools are: the stake floor needs stake on forging pools
	// from the first epoch, which pools registering from nothing cannot
	// give it.
	for _, p := range file.Populations {
		if p.Kind == profile.Pool {
			cfg.GenesisPools += p.Count
		}
	}
	return cfg, nil
}

// Oracle names the run in findings' replay commands: the log as run --out
// writes it, and every flag that fixes the configuration, so the command
// replays it from the run's directory. It is built from the parsed values,
// so run and replay give the same one however the flags were spelled.
func (f *Flags) Oracle(mistakes bool, fork protocol.Schedule) oracle.Run {
	onOff := map[bool]string{true: "on", false: "off"}
	flags := []string{
		"--wallets=" + strconv.Itoa(f.Wallets), "--profiles=" + f.Profiles,
		"--scale=" + strconv.FormatFloat(f.Scale, 'g', -1, 64), "--slot-ms=" + strconv.FormatInt(f.SlotMs, 10),
		"--epoch-slots=" + strconv.FormatUint(f.EpochSlots, 10), "--k=" + strconv.FormatUint(f.K, 10),
		"--block-every=" + strconv.FormatUint(f.BlockEvery, 10), "--ttl=" + strconv.FormatUint(f.TTL, 10),
		"--floor=" + strconv.FormatUint(f.Floor, 10), "--pools=" + strconv.Itoa(f.Pools), "--mistakes=" + onOff[mistakes],
		"--hard-fork=" + fork.String(),
	}
	if f.Attacks != "" {
		flags = append(flags, "--attack="+f.Attacks)
	}
	return oracle.Run{Seed: f.Seed, Log: "events.log", Flags: flags}
}

// Load reads the population: the profile set, its cohorts, and the file.
func (f *Flags) Load() (*profile.Registry, []engine.Cohort, profile.File, error) {
	reg, file, err := f.parse()
	if err != nil {
		return nil, nil, profile.File{}, err
	}
	cohorts, err := f.cohorts(reg)
	if err != nil {
		return nil, nil, profile.File{}, err
	}
	ps, cohorts, err := f.WithAdversaries(file.Profiles, cohorts)
	if err != nil {
		return nil, nil, profile.File{}, err
	}
	if reg, err = profile.NewRegistry(ps); err != nil {
		return nil, nil, profile.File{}, err
	}
	return reg, cohorts, file, nil
}
