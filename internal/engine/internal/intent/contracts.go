package intent

import (
	"fmt"
	"math/rand/v2"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Genesis is how many contracts exist from genesis: the shared
// AlwaysSucceeds lock and one instance of each template (build.Template),
// so every run calls the vesting and escrow validators.
const Genesis = 3

// Contracts are the deployed contracts.
//
// Contracts 0 to Genesis-1 exist from genesis. A file with a
// contract population deploys more at its new_rate per genesis wallet per
// day: a deployment is the moment its address becomes one wallets lock at,
// so it has no transaction of its own. Each contract draws a usage weight
// from the population's Pareto, and a lock picks its contract in
// proportion to the weights, so a few contracts take most calls.
type Contracts struct {
	core
	// n is how many contracts are deployed (Genesis of them from genesis); at is
	// the next deployment, drawn its draw.
	n     uint64
	at    sched.Time
	drawn uint64
}

// Begin schedules the first deployment, if the file deploys any.
func (ct *Contracts) Begin(now sched.Time) {
	if ct.spec.DeployRate > 0 {
		ct.schedule(now)
	}
}

func (ct *Contracts) schedule(now sched.Time) {
	ct.drawn++
	r := ct.spec.Seed.Derive("deploy", strconv.FormatUint(ct.drawn, 10)).Rand()
	ct.at = sched.NextArrival(r, ct.spec.DeployRate, nil, now)
	ct.push()
}

func (ct *Contracts) push() {
	if ct.at != population.Never && ct.at != 0 {
		ct.env.Clock.Deploy(ct.at)
	}
}

// Deploy makes the next contract available.
func (ct *Contracts) Deploy(at sched.Time) error {
	i := ct.n
	if err := ct.book(i); err != nil {
		return err
	}
	ct.n++
	c := build.Contract(i)
	ct.env.Journal.Emit(at, wire.KindDeployed, 0, wire.Deployed{Contract: i, Address: c.Name, Validator: c.Validator, Script: c.Hash().String()})
	ct.schedule(at)
	return nil
}

func (ct *Contracts) book(i uint64) error {
	if err := ct.env.Chain.Book().AddAddress(build.Contract(i).Address(ct.spec.NetworkID), ct.spec.Script); err != nil {
		return fmt.Errorf("engine: book contract %d: %w", i, err)
	}
	return nil
}

// Count is the number of contracts deployed, the genesis ones included.
func (ct *Contracts) Count() int { return int(ct.n) }

// usage is contract i's usage weight.
func (ct *Contracts) usage(i uint64) float64 {
	cp := ct.spec.Contract
	if cp == nil {
		return 1
	}
	return cp.Usage.Sample(ct.spec.Seed.Derive("contract", strconv.FormatUint(i, 10)).Rand())
}

// pick draws the contract a lock goes to, in proportion to the
// contracts' usage weights (uniform without a contract population).
func (ct *Contracts) pick(r *rand.Rand) uint64 {
	if ct.n <= 1 {
		return 0
	}
	ws := make([]float64, ct.n)
	var sum float64
	for i := range ws {
		ws[i] = ct.usage(uint64(i))
		sum += ws[i]
	}
	u := r.Float64() * sum
	for i, w := range ws {
		if u < w {
			return uint64(i)
		}
		u -= w
	}
	return ct.n - 1
}

// State is the deployment state a snapshot keeps.
type State struct {
	// N is how many contracts are deployed, at least Genesis.
	N uint64 `json:"n,omitzero"`
	// At is the next deployment.
	At sched.Time `json:"at,omitzero"`
	// Drawn is how many times the next deployment's time was drawn.
	Drawn uint64 `json:"drawn,omitzero"`
}

// Save is the state a snapshot keeps.
func (ct *Contracts) Save() State { return State{N: ct.n, At: ct.at, Drawn: ct.drawn} }

// Load restores the state of a snapshot: it books the deployed contracts'
// addresses again (the engine books the genesis ones) and schedules the
// next deployment.
func (ct *Contracts) Load(s State) error {
	ct.n, ct.at, ct.drawn = max(s.N, Genesis), s.At, s.Drawn
	for i := uint64(Genesis); i < ct.n; i++ {
		if err := ct.book(i); err != nil {
			return err
		}
	}
	ct.push()
	return nil
}
