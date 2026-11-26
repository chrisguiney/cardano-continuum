package explorer

import (
	"encoding/hex"
	"slices"
	"strings"
)

// epochAgg is one epoch's totals.
type epochAgg struct {
	blocks, txs, fees, size uint64
	first, last             uint64
	byPool                  map[[28]byte]uint64
}

// epochs aggregates blocks per epoch, so a summary needs no scan.
type epochs struct {
	m map[uint64]*epochAgg
}

func newEpochs() *epochs { return &epochs{m: map[uint64]*epochAgg{}} }

func (e *epochs) add(ep uint64, m *meta) {
	a := e.m[ep]
	if a == nil {
		a = &epochAgg{first: m.height, byPool: map[[28]byte]uint64{}}
		e.m[ep] = a
	}
	a.blocks++
	a.txs += uint64(len(m.txs))
	a.fees += m.fees
	a.size += m.size
	a.last = m.height
	a.byPool[m.issuer]++
}

// sub undoes add for the newest block of its epoch.
func (e *epochs) sub(ep uint64, m *meta) {
	a := e.m[ep]
	if a == nil {
		return
	}
	a.blocks--
	a.txs -= uint64(len(m.txs))
	a.fees -= m.fees
	a.size -= m.size
	if a.byPool[m.issuer]--; a.byPool[m.issuer] == 0 {
		delete(a.byPool, m.issuer)
	}
	if a.blocks == 0 {
		delete(e.m, ep)
		return
	}
	a.last = m.height - 1
}

// view is the epoch's summary. Expected is an equal share of the epoch's
// blocks among pools, or among the known pools when known is not empty: the
// index does not see stake, so it is an equal-stake estimate.
func (e *epochs) view(ep uint64, known []string, res func(string) *PoolRef) (EpochView, bool) {
	a := e.m[ep]
	if a == nil {
		return EpochView{}, false
	}
	v := EpochView{Epoch: ep, Blocks: a.blocks, Txs: a.txs, Fees: a.fees, Size: a.size, First: a.first, Last: a.last, Pools: []EpochPool{}}
	ids := map[string]uint64{}
	for p, n := range a.byPool {
		ids[hex.EncodeToString(p[:])] = n
	}
	for _, k := range known {
		if _, ok := ids[k]; !ok {
			ids[k] = 0
		}
	}
	for id, n := range ids {
		v.Pools = append(v.Pools, EpochPool{Pool: id, Resolved: res(id), Blocks: n, Expected: float64(a.blocks) / float64(len(ids))})
	}
	slices.SortFunc(v.Pools, func(x, y EpochPool) int {
		if x.Blocks != y.Blocks {
			return int(y.Blocks) - int(x.Blocks)
		}
		return strings.Compare(x.Pool, y.Pool)
	})
	return v, true
}
