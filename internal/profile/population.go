package profile

import (
	"fmt"
	"math"
)

// Population is a fitted population of non-wallet actors: stake pools,
// DReps or script contracts. Wallet behaviour lives in Profile; a
// Population says how many of the other kinds there are, how fast they
// appear and leave, and how their parameters are spread, as lab/fit.py
// measured them on a real chain.
//
// One machine cannot run a mainnet's thousands of pools, so Count is the
// number to run and ChainCount the number the chain had. Per-member rates
// and distributions are scale-free and stay as fitted; Scale says how far
// the head count was cut.
type Population struct {
	Name string
	Kind Kind
	// Count is how many members a run starts with; zero means the kind is
	// not run as actors (contracts are deployed by wallets' locks).
	Count int
	// ChainCount is how many members the chain had in the fitted window.
	ChainCount int
	// Exactly one of the parameter blocks is set, the one matching Kind.
	Pool     *PoolParams
	DRep     *DRepParams
	Contract *ContractParams
}

// Scale is Count over ChainCount: the share of the chain's members a run
// keeps. A population that is not cut down (Count zero) has scale 1.
func (p Population) Scale() float64 {
	if p.Count == 0 || p.ChainCount == 0 {
		return 1
	}
	return float64(p.Count) / float64(p.ChainCount)
}

// PoolParams are a stake-pool population's rates and registration
// parameters. Rates are events per registered pool per day.
type PoolParams struct {
	// Register is new pools per registered pool per day.
	Register float64
	// Retire is retirement certificates per registered pool per day.
	Retire float64
	// Update is re-registrations (parameter changes) per pool per day.
	Update float64
	// Vote is votes cast by the pool as an SPO voter per registered pool
	// per day: spo_vote_rate, fitted from the chain's stake_pool votes in
	// the window (lab/actor_fit.py). Zero, as in a file fitted before it
	// existed, means the pools never vote.
	Vote float64
	// Pledge and Cost are in lovelace; Margin is a fraction in [0, 1].
	Pledge, Margin, Cost Spike
}

// DRepParams are a DRep population's rates. Rates are events per
// registered DRep per day.
type DRepParams struct {
	Register float64
	// Retire is deregistrations per registered DRep per day.
	Retire float64
	// Vote is votes cast per registered DRep per day.
	Vote float64
	// Voting is the share of registered DReps that voted at all.
	Voting float64
}

// ContractParams describe script addresses: how fast new ones appear and
// how unevenly use is spread over them.
type ContractParams struct {
	// New is new script addresses per active wallet unit per day, so it
	// scales with the number of wallets a run has.
	New float64
	// Usage is the transactions per script address over the window. A
	// Pareto index under 1 means a few contracts take nearly all use.
	Usage Pareto
	// TopShare is the share of script transactions that go to the busiest
	// 1% of script addresses.
	TopShare float64
}

// Spike is a distribution with a point mass: Point with probability Share,
// otherwise a draw from Rest. Pool parameters need it because many pools
// sit exactly on a value (zero pledge, the minimum fixed cost).
type Spike struct {
	Rest  Dist
	Point float64
	Share float64
}

// Sample draws one value.
func (s Spike) Sample(src Source) float64 {
	if src.Float64() < s.Share {
		return s.Point
	}
	return s.Rest.Sample(src)
}

// Network holds chain-wide rates that belong to no actor.
type Network struct {
	// Proposals is governance proposals submitted per day, by action type
	// (the Conway action names mithril2pq's proposal table uses).
	Proposals map[string]float64
}

// File is everything a profile file holds.
type File struct {
	Profiles    []Profile
	Populations []Population
	Network     Network
	// Markets is the ADA market the wallets trade in; nil means none.
	Markets *Markets
	// Humans is the people who own the wallets; nil means every wallet is
	// its own owner.
	Humans *Humans
}

var actionTypes = map[string]bool{
	"parameter_change": true, "hard_fork_initiation": true, "treasury_withdrawals": true,
	"no_confidence": true, "update_committee": true, "new_constitution": true, "info": true,
}

type rawPopulation struct {
	Name       string       `yaml:"name"`
	Kind       string       `yaml:"kind"`
	Count      int          `yaml:"count,omitempty"`
	ChainCount int          `yaml:"chain_count"`
	Pool       *rawPool     `yaml:"pool,omitempty"`
	DRep       *rawDRep     `yaml:"drep,omitempty"`
	Contract   *rawContract `yaml:"contract,omitempty"`
}

type rawSpike struct {
	Dist       string  `yaml:"dist"`
	Point      float64 `yaml:"point,omitempty"`
	PointShare float64 `yaml:"point_share,omitempty"`
}

type rawPool struct {
	Register float64  `yaml:"register_rate"`
	Retire   float64  `yaml:"retire_rate"`
	Update   float64  `yaml:"update_rate"`
	Vote     float64  `yaml:"spo_vote_rate,omitempty"`
	Pledge   rawSpike `yaml:"pledge"`
	Margin   rawSpike `yaml:"margin"`
	Cost     rawSpike `yaml:"cost"`
}

type rawDRep struct {
	Register float64 `yaml:"register_rate"`
	Retire   float64 `yaml:"retire_rate"`
	Vote     float64 `yaml:"vote_rate"`
	Voting   float64 `yaml:"voting_share"`
}

type rawContract struct {
	New      float64 `yaml:"new_rate"`
	Usage    string  `yaml:"usage"`
	TopShare float64 `yaml:"top_share"`
}

type rawNetwork struct {
	Proposals map[string]float64 `yaml:"proposal_rate,omitempty"`
}

func share(field string, v float64) error {
	if math.IsNaN(v) || v < 0 || v > 1 {
		return fieldErr(field, "must be in [0, 1], got %s", ff(v))
	}
	return nil
}

func rates(prefix string, named map[string]float64) error {
	for _, k := range sortedKeys(named) {
		if err := finiteNonNeg(prefix+k, named[k]); err != nil {
			return err
		}
	}
	return nil
}

func validatePopulations(raw []rawPopulation) ([]Population, error) {
	out := make([]Population, 0, len(raw))
	seen := map[string]bool{}
	for i, rp := range raw {
		p, err := rp.validate()
		if err == nil && seen[rp.Name] {
			err = fieldErr("name", "%q is used twice", rp.Name)
		}
		if err != nil {
			label := fmt.Sprintf("population[%d]", i)
			if rp.Name != "" {
				label = fmt.Sprintf("population %q", rp.Name)
			}
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		seen[rp.Name] = true
		out = append(out, p)
	}
	return out, nil
}

func (rp rawPopulation) validate() (Population, error) {
	p := Population{Name: rp.Name, Kind: Kind(rp.Kind), Count: rp.Count, ChainCount: rp.ChainCount}
	if !identRE.MatchString(rp.Name) {
		return p, fieldErr("name", "%q must match %s", rp.Name, identRE)
	}
	if rp.Count < 0 || rp.ChainCount < 0 {
		return p, fieldErr("count", "counts must be >= 0, got %d of %d", rp.Count, rp.ChainCount)
	}
	// A run may cut a population down, never grow it: more members than the
	// chain had would make the per-member rates describe a different network.
	if rp.Count > rp.ChainCount {
		return p, fieldErr("count", "%d is above chain_count %d", rp.Count, rp.ChainCount)
	}
	var err error
	switch blocks := countSet(rp.Pool != nil, rp.DRep != nil, rp.Contract != nil); {
	case p.Kind == Pool && rp.Pool != nil && blocks == 1:
		p.Pool, err = rp.Pool.validate()
	case p.Kind == DRep && rp.DRep != nil && blocks == 1:
		p.DRep, err = rp.DRep.validate()
	case p.Kind == Contract && rp.Contract != nil && blocks == 1:
		p.Contract, err = rp.Contract.validate()
	case p.Kind == Pool || p.Kind == DRep || p.Kind == Contract:
		err = fieldErr(string(p.Kind), "a %s population needs its %s block and no other", p.Kind, p.Kind)
	default:
		err = fieldErr("kind", "%q is not pool, drep or contract", rp.Kind)
	}
	return p, err
}

func countSet(bs ...bool) int {
	n := 0
	for _, b := range bs {
		if b {
			n++
		}
	}
	return n
}

func (rp rawPool) validate() (*PoolParams, error) {
	if err := rates("pool.", map[string]float64{
		"register_rate": rp.Register, "retire_rate": rp.Retire, "update_rate": rp.Update, "spo_vote_rate": rp.Vote,
	}); err != nil {
		return nil, err
	}
	p := &PoolParams{Register: rp.Register, Retire: rp.Retire, Update: rp.Update, Vote: rp.Vote}
	for _, f := range []struct {
		name string
		raw  rawSpike
		dst  *Spike
	}{
		{"pool.pledge", rp.Pledge, &p.Pledge},
		{"pool.margin", rp.Margin, &p.Margin},
		{"pool.cost", rp.Cost, &p.Cost},
	} {
		s, err := f.raw.validate(f.name)
		if err != nil {
			return nil, err
		}
		*f.dst = s
	}
	return p, nil
}

func (rs rawSpike) validate(field string) (Spike, error) {
	d, err := parseDist(rs.Dist)
	if err != nil {
		return Spike{}, fieldErr(field+".dist", "%v", err)
	}
	if err := finiteNonNeg(field+".point", rs.Point); err != nil {
		return Spike{}, err
	}
	if err := share(field+".point_share", rs.PointShare); err != nil {
		return Spike{}, err
	}
	return Spike{Rest: d, Point: rs.Point, Share: rs.PointShare}, nil
}

func (rd rawDRep) validate() (*DRepParams, error) {
	if err := rates("drep.", map[string]float64{
		"register_rate": rd.Register, "retire_rate": rd.Retire, "vote_rate": rd.Vote,
	}); err != nil {
		return nil, err
	}
	if err := share("drep.voting_share", rd.Voting); err != nil {
		return nil, err
	}
	return &DRepParams{Register: rd.Register, Retire: rd.Retire, Vote: rd.Vote, Voting: rd.Voting}, nil
}

func (rc rawContract) validate() (*ContractParams, error) {
	if err := finiteNonNeg("contract.new_rate", rc.New); err != nil {
		return nil, err
	}
	d, err := parseDist(rc.Usage)
	if err != nil {
		return nil, fieldErr("contract.usage", "%v", err)
	}
	usage, ok := d.(Pareto)
	if !ok {
		return nil, fieldErr("contract.usage", "must be a pareto distribution, got %s", d)
	}
	if err := share("contract.top_share", rc.TopShare); err != nil {
		return nil, err
	}
	return &ContractParams{New: rc.New, Usage: usage, TopShare: rc.TopShare}, nil
}

func (rn rawNetwork) validate() (Network, error) {
	var n Network
	if len(rn.Proposals) == 0 {
		return n, nil
	}
	n.Proposals = make(map[string]float64, len(rn.Proposals))
	for _, k := range sortedKeys(rn.Proposals) {
		if !actionTypes[k] {
			return n, fieldErr("network.proposal_rate."+k, "unknown governance action type")
		}
		if err := finiteNonNeg("network.proposal_rate."+k, rn.Proposals[k]); err != nil {
			return n, err
		}
		n.Proposals[k] = rn.Proposals[k]
	}
	return n, nil
}
