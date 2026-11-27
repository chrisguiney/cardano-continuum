package chain

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// gen drives a random chain: blocks of random transactions over owned and
// foreign addresses and credentials, interleaved with rollbacks no deeper
// than the view's settled point.
type gen struct {
	t     *testing.T
	r     *rand.Rand
	k     uint64
	addrs []common.Address
	nextT uint64
	salt  uint64
}

func (g *gen) tx(v *View, used map[Ref]bool) Tx {
	g.nextT++
	id := txid(1000 + g.nextT)
	var ins []Ref
	for _, u := range v.Snapshot().Utxos {
		ref := RefOf(u.Id)
		if !used[ref] && g.r.IntN(3) == 0 && len(ins) < 2 {
			used[ref] = true
			ins = append(ins, ref)
		}
	}
	if g.r.IntN(4) == 0 {
		ins = append(ins, Ref{txid(g.r.Uint64N(1 << 20)), 0}) // foreign input
	}
	var pays []pay
	for range 1 + g.r.IntN(2) {
		pays = append(pays, pay{g.addrs[g.r.IntN(len(g.addrs))], 1 + g.r.Uint64N(1000)})
	}
	tx := mkTx(id, ins, pays...)
	if g.r.IntN(3) == 0 {
		tx.Certs = append(tx.Certs, g.cert())
	}
	return tx
}

func (g *gen) cert() common.Certificate {
	drepCred := hash224(8)
	cred := common.Credential{Credential: hash224([]byte{1, 2, 70}[g.r.IntN(3)])}
	pool := hash224([]byte{9, 60}[g.r.IntN(2)])
	switch g.r.IntN(7) {
	case 0:
		return &common.StakeRegistrationCertificate{StakeCredential: cred}
	case 1:
		return &common.StakeDeregistrationCertificate{StakeCredential: cred}
	case 2:
		return &common.StakeDelegationCertificate{StakeCredential: &cred, PoolKeyHash: pool}
	case 3:
		return &common.PoolRegistrationCertificate{Operator: pool}
	case 4:
		return &common.PoolRetirementCertificate{PoolKeyHash: pool, Epoch: g.r.Uint64N(3)}
	case 5:
		return &common.StakeVoteRegistrationDelegationCertificate{
			StakeCredential: cred, PoolKeyHash: pool, Drep: common.Drep{Type: 0, Credential: drepCred[:]},
		}
	default:
		return &common.RegistrationDrepCertificate{DrepCredential: common.Credential{Credential: hash224(8)}}
	}
}

func (g *gen) block(v *View, chain []Block) Block {
	used := map[Ref]bool{}
	var txs []Tx
	for range g.r.IntN(4) {
		txs = append(txs, g.tx(v, used))
	}
	g.salt++
	var b Block
	if len(chain) == 0 {
		b = next(nil, g.salt, txs...)
	} else {
		b = next(&chain[len(chain)-1], g.salt, txs...)
	}
	// Skip slots now and then so blocks cross epoch boundaries.
	if gap := g.r.Uint64N(40); gap > 0 {
		b.Point.Slot += gap
		b.Point.Hash = common.Blake2b256Hash(append(b.Point.Hash[:], byte(gap)))
	}
	return b
}

func fold(t *testing.T, k uint64, gen []common.Utxo, chain []Block) *View {
	t.Helper()
	v := newView(t, k, gen...)
	for _, b := range chain {
		mustApply(t, v, b)
	}
	return v
}

// PropApplyRollbackInverse: after any interleaving of blocks and rollbacks
// within the settled point, the view equals a fresh fold of the chain it is
// on; the settled point never moves back and is always on that chain.
func TestPropApplyRollbackInverse(t *testing.T) {
	t.Parallel()
	for seed := range uint64(150) {
		k := 1 + seed%4
		r := rand.New(rand.NewPCG(seed, 7))
		g := &gen{t: t, r: r, k: k, addrs: []common.Address{addrOf(t, 1), addrOf(t, 2), addrOf(t, 50)}}
		genesis := []common.Utxo{utxo(txid(0), 0, g.addrs[0], 5000), utxo(txid(0), 1, g.addrs[1], 5000)}
		v := newView(t, k, genesis...)
		var chain []Block
		var settledH uint64
		for step := range 50 {
			chain = g.step(v, chain, settledH)
			want := fold(t, k, genesis, chain)
			if got := v.Snapshot(); !reflect.DeepEqual(got, want.Snapshot()) {
				t.Fatalf("seed %d step %d: view differs from fold\n got %+v\nwant %+v", seed, step, got, want.Snapshot())
			}
			h := heightOf(chain, v.Settled())
			if h < settledH {
				t.Fatalf("seed %d step %d: settled moved back from %d to %d", seed, step, settledH, h)
			}
			if h > uint64(len(chain)) || (h > 0 && chain[h-1].Point != v.Settled()) {
				t.Fatalf("seed %d step %d: settled %v not on the chain", seed, step, v.Settled())
			}
			settledH = h
		}
	}
}

func (g *gen) step(v *View, chain []Block, settledH uint64) []Block {
	depth := uint64(len(chain)) - settledH
	if depth > 0 && g.r.IntN(4) == 0 {
		n := settledH + g.r.Uint64N(depth)
		to := Point{}
		if n > 0 {
			to = chain[n-1].Point
		}
		if _, err := v.Rollback(to); err != nil {
			g.t.Fatalf("rollback to height %d (settled %d): %v", n, settledH, err)
		}
		return chain[:n]
	}
	b := g.block(v, chain)
	mustApply(g.t, v, b)
	return append(chain, b)
}

func heightOf(chain []Block, p Point) uint64 {
	for i, b := range chain {
		if b.Point == p {
			return uint64(i + 1)
		}
	}
	return 0
}
