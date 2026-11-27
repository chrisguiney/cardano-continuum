package policy

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// This file transliterates Allowed, Adds and Endangered from
// models/specs/stake-floor/StakeFloor.tla, with the mutation switches at
// "none", over the model's domain: three pools, four wallets, w1 holding 3
// units and the others 1. The margin is a subset enumeration, as in
// OkWithMargin, rather than the guard's largest-first removal, so the two
// sides compute it independently.

const (
	nPools   = 3
	nWallets = 4
	none     = -1 // a wallet delegated nowhere
	noTx     = -2 // nothing in flight for a wallet
)

var mStake = [nWallets]int{3, 1, 1, 1}

type reg int

const (
	mRegistered reg = iota
	mRetiring
	mRetired
)

type model struct {
	floor, margin    int
	epoch, label     int
	deleg, mark, set [nWallets]int
	reg              [nPools]reg
	plan             [nPools]bool // true: live
	failed           [nPools]bool
	pend             [nWallets]int
	pret             [nPools]bool
	preg             [nPools]bool
}

func (m model) ok(d [nWallets]int, f [nPools]bool) bool {
	tot, fs := 0, 0
	for w, p := range d {
		if p == none {
			continue
		}
		tot += mStake[w]
		if f[p] {
			fs += mStake[w]
		}
	}
	return tot > 0 && 100*fs >= m.floor*tot
}

func (m model) okWithMargin(d [nWallets]int, f [nPools]bool) bool {
	forging := 0
	for p := range nPools {
		if f[p] {
			forging |= 1 << p
		}
	}
	for lost := 0; lost < 1<<nPools; lost++ {
		if lost&^forging != 0 {
			continue // L must be a subset of F
		}
		g, n := f, 0
		for p := range nPools {
			if lost&(1<<p) != 0 {
				g[p] = false
				n++
			}
		}
		if n <= m.margin && !m.ok(d, g) {
			return false
		}
	}
	return true
}

func (m model) guardForging(plan [nPools]bool, r [nPools]reg) [nPools]bool {
	var f [nPools]bool
	for p := range nPools {
		f[p] = plan[p] && !m.failed[p] && r[p] == mRegistered
	}
	return f
}

func (m model) afterPending(pd [nWallets]int) [nWallets]int {
	out := m.deleg
	for w, q := range pd {
		if q != noTx {
			out[w] = q
		}
	}
	return out
}

func (m model) regAfter(pr [nPools]bool) [nPools]reg {
	out := m.reg
	for p, r := range pr {
		if r {
			out[p] = mRetiring
		}
	}
	return out
}

func (m model) horizon(d2 [nWallets]int) [][nWallets]int {
	if m.epoch > m.label+1 {
		return [][nWallets]int{m.set}
	}
	lead, next := m.set, m.mark
	if m.epoch == m.label+1 {
		lead, next = m.mark, m.deleg
	}
	return [][nWallets]int{lead, next, d2}
}

func (m model) allowed(plan [nPools]bool, pd [nWallets]int, pr [nPools]bool) bool {
	f := m.guardForging(plan, m.regAfter(pr))
	for _, d := range m.horizon(m.afterPending(pd)) {
		if !m.okWithMargin(d, f) {
			return false
		}
	}
	return true
}

func (m model) adds(w, q int) bool {
	f := m.guardForging(m.plan, m.regAfter(m.pret))
	return q != none && f[q] && m.deleg[w] != none && !f[m.deleg[w]]
}

func (m model) endangered() bool {
	var f [nPools]bool
	for p := range nPools {
		f[p] = m.plan[p] && !m.failed[p] && m.reg[p] == mRegistered
	}
	ds := m.horizon(m.afterPending(m.pend))
	for _, d := range ds {
		if !m.ok(d, f) {
			return true
		}
	}
	return false
}

func randModel(r *rand.Rand) model {
	m := model{
		floor:  []int{30, 50, 70, 100}[r.IntN(4)],
		margin: r.IntN(3),
		label:  r.IntN(4),
	}
	m.epoch = m.label + r.IntN(3)
	// Mostly the genesis delegation, perturbed, so that both answers are
	// common.
	genesis := [nWallets]int{0, 1, 2, 2}
	target := func(w int) int {
		if r.IntN(3) != 0 {
			return genesis[w]
		}
		return r.IntN(nPools+1) - 1
	}
	for w := range nWallets {
		m.deleg[w], m.mark[w], m.set[w] = target(w), target(w), target(w)
		m.pend[w] = noTx
		if r.IntN(3) == 0 {
			m.pend[w] = target(w)
		}
	}
	for p := range nPools {
		m.reg[p] = []reg{mRegistered, mRegistered, mRegistered, mRegistered, mRetiring, mRetired}[r.IntN(6)]
		m.plan[p] = r.IntN(6) != 0
		m.failed[p] = r.IntN(10) == 0
		m.pret[p] = m.reg[p] == mRegistered && r.IntN(5) == 0
		m.preg[p] = m.reg[p] == mRetiring && r.IntN(3) == 0
	}
	return m
}

func poolID(p int) population.ActorID {
	if p == none {
		return 0
	}
	return population.ActorID(p + 1)
}

func walletID(w int) population.ActorID { return population.ActorID(w + 11) }

func (m model) schedule(d [nWallets]int) Schedule {
	out := make(Schedule, 0, nWallets)
	for w, p := range d {
		out = append(out, Delegation{Wallet: walletID(w), Pool: poolID(p), Stake: uint64(mStake[w])})
	}
	return out
}

// state maps the model's variables onto the guard's input.
func (m model) state() State {
	s := State{
		Epoch: uint64(m.epoch), Label: uint64(m.label),
		Set: m.schedule(m.set), Mark: m.schedule(m.mark), Deleg: m.schedule(m.deleg),
	}
	for p := range nPools {
		s.Pools = append(s.Pools, Pool{ID: poolID(p), Reg: [...]Reg{Registered, Retiring, Retired}[m.reg[p]], Live: m.plan[p], Running: !m.failed[p]})
		if m.pret[p] {
			s.InFlight = append(s.InFlight, Intent{Op: Retire, Pool: poolID(p)})
		}
		if m.preg[p] {
			s.InFlight = append(s.InFlight, Intent{Op: Reregister, Pool: poolID(p)})
		}
	}
	for w, q := range m.pend {
		switch q {
		case noTx:
		case none:
			s.InFlight = append(s.InFlight, Intent{Op: Undelegate, Wallet: walletID(w)})
		default:
			s.InFlight = append(s.InFlight, Intent{Op: Delegate, Wallet: walletID(w), Pool: poolID(q)})
		}
	}
	return s
}

type modelCase struct {
	in   Intent
	want bool
}

// cases lists every guarded engine step the model can take in m, with the
// model's answer: GoDark, Retire and Delegate, under their preconditions.
func (m model) cases() []modelCase {
	var out []modelCase
	for p := range nPools {
		if m.plan[p] {
			pl := m.plan
			pl[p] = false
			out = append(out, modelCase{Intent{Op: GoDark, Pool: poolID(p)}, m.allowed(pl, m.pend, m.pret)})
		}
		if m.reg[p] == mRegistered && !m.pret[p] && !m.preg[p] {
			pr := m.pret
			pr[p] = true
			out = append(out, modelCase{Intent{Op: Retire, Pool: poolID(p)}, m.allowed(m.plan, m.pend, pr)})
		}
	}
	for w := range nWallets {
		if m.pend[w] != noTx {
			continue
		}
		for q := none; q < nPools; q++ {
			if q == m.deleg[w] || (q != none && m.reg[q] == mRetired) {
				continue
			}
			pd := m.pend
			pd[w] = q
			want := m.adds(w, q) || m.allowed(m.plan, pd, m.pret)
			in := Intent{Op: Delegate, Wallet: walletID(w), Pool: poolID(q)}
			if q == none {
				in = Intent{Op: Undelegate, Wallet: walletID(w)}
			}
			out = append(out, modelCase{in, want})
		}
	}
	return out
}

func TestPolicyAgreesWithModel(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(31, 1))
	checked, allowed := 0, 0
	for range 40_000 {
		m := randModel(r)
		s := m.state()
		g := guard(uint64(m.floor), m.margin)
		if got := g.Endangered(s); got != m.endangered() {
			t.Fatalf("Endangered = %v, model %v, in %+v", got, !got, m)
		}
		for _, c := range m.cases() {
			v := g.Check(s, c.in)
			if v.OK != c.want {
				t.Fatalf("%+v: guard %+v, model %v, in %s", c.in, v, c.want, fmt.Sprintf("%+v", m))
			}
			checked++
			if c.want {
				allowed++
			}
		}
	}
	// Both answers must be common, or the comparison proves little.
	if allowed < checked/10 || allowed > checked*9/10 {
		t.Fatalf("%d of %d allowed: the domain is lopsided", allowed, checked)
	}
}
