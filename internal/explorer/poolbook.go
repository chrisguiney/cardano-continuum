package explorer

import (
	"encoding/hex"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// PoolReg is a pool's registration terms as its latest certificate states
// them.
type PoolReg struct {
	// Pledge is the lovelace the owners promise to keep delegated.
	Pledge uint64 `json:"pledge"`
	// Cost is the fixed lovelace the pool takes from each epoch's rewards.
	Cost uint64 `json:"cost"`
	// Margin is the share of the remaining rewards the pool takes, 0 to 1.
	Margin float64 `json:"margin"`
	// RewardAccount is the stake key hash (hex) rewards are paid to.
	RewardAccount string `json:"reward_account"`
	// Owners counts the owner stake keys.
	Owners int `json:"owners"`
	// MetadataURL is where the pool's name and ticker are published; the
	// index does not fetch it.
	MetadataURL string `json:"metadata_url,omitempty"`
	// Slot is the slot of the block that carried the certificate.
	Slot uint64 `json:"slot"`
}

// PoolStat is what the chain shows of one stake pool.
type PoolStat struct {
	// ID is the pool id, hex.
	ID string `json:"id"`
	// Blocks counts the blocks the index holds that the pool issued.
	Blocks uint64 `json:"blocks"`
	// Reg is the latest registration; nil for a pool the index saw no
	// certificate for, such as a genesis pool.
	Reg *PoolReg `json:"registration,omitempty"`
	// RetireEpoch is the epoch a pending or applied retirement takes
	// effect; nil when none is in force.
	RetireEpoch *uint64 `json:"retire_epoch,omitempty"`
}

// poolCert is one pool certificate a block carries: a registration (reg
// set) or a retirement.
type poolCert struct {
	pool   [28]byte
	reg    *PoolReg
	retire uint64
}

// poolBook keeps the pool certificates of the blocks on the chain, so a
// rollback drops exactly those of the blocks it removes. The index lock
// covers it.
type poolBook struct {
	byBlock map[[32]byte][]poolCert
	// order is the blocks with certificates, oldest first.
	order [][32]byte
}

func newPoolBook() *poolBook { return &poolBook{byBlock: map[[32]byte][]poolCert{}} }

// add records a block's pool certificates. An invalid transaction's
// certificates do not apply.
func (b *poolBook) add(block [32]byte, slot uint64, txs []common.Transaction) {
	var cs []poolCert
	for _, tx := range txs {
		if !tx.IsValid() {
			continue
		}
		for _, c := range tx.Certificates() {
			if pc, ok := poolCertOf(c, slot); ok {
				cs = append(cs, pc)
			}
		}
	}
	if len(cs) == 0 {
		return
	}
	b.byBlock[block] = cs
	b.order = append(b.order, block)
}

// drop forgets a rolled-back block's certificates.
func (b *poolBook) drop(block [32]byte) {
	if _, ok := b.byBlock[block]; !ok {
		return
	}
	delete(b.byBlock, block)
	b.order = slices.DeleteFunc(b.order, func(h [32]byte) bool { return h == block })
}

// poolCertOf is c as a pool certificate, if it is one.
func poolCertOf(c common.Certificate, slot uint64) (poolCert, bool) {
	switch x := c.(type) {
	case *common.PoolRegistrationCertificate:
		return poolCert{pool: x.Operator, reg: regOf(x, slot)}, true
	case *common.PoolRetirementCertificate:
		return poolCert{pool: x.PoolKeyHash, retire: x.Epoch}, true
	}
	return poolCert{}, false
}

func regOf(x *common.PoolRegistrationCertificate, slot uint64) *PoolReg {
	r := &PoolReg{
		Pledge:        x.Pledge,
		Cost:          x.Cost,
		RewardAccount: hex.EncodeToString(x.RewardAccount[:]),
		Owners:        len(x.PoolOwners),
		Slot:          slot,
	}
	if x.Margin.Rat != nil {
		r.Margin, _ = x.Margin.Float64()
	}
	if x.PoolMetadata != nil {
		r.MetadataURL = x.PoolMetadata.Url
	}
	return r
}

// stats folds the certificates in chain order and adds each issuer's block
// count. A registration cancels a pending retirement, as the ledger's POOL
// rule does.
func (b *poolBook) stats(byIssuer map[[28]byte][]int) []PoolStat {
	out := map[[28]byte]*PoolStat{}
	stat := func(id [28]byte) *PoolStat {
		if out[id] == nil {
			out[id] = &PoolStat{ID: hex.EncodeToString(id[:])}
		}
		return out[id]
	}

	for _, h := range b.order {
		for _, c := range b.byBlock[h] {
			s := stat(c.pool)
			if c.reg != nil {
				s.Reg, s.RetireEpoch = c.reg, nil
				continue
			}
			ep := c.retire
			s.RetireEpoch = &ep
		}
	}

	for id, blocks := range byIssuer {
		stat(id).Blocks = uint64(len(blocks))
	}

	ids := slices.SortedFunc(maps.Keys(out), func(a, b [28]byte) int { return slices.Compare(a[:], b[:]) })
	list := make([]PoolStat, 0, len(ids))
	for _, id := range ids {
		list = append(list, *out[id])
	}
	return list
}
