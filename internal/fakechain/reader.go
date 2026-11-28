package fakechain

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// reader answers the read-only queries. Chain embeds it, so these methods
// are Chain's; each takes the world's lock itself.
type reader struct{ w *world }

// Slot returns the clock's current slot.
func (rd reader) Slot() uint64 {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.slot
}

// Epoch returns the epoch of the current slot.
func (rd reader) Epoch() uint64 { return rd.w.cfg.Slots.EpochOfSlot(rd.Slot()) }

// Tip returns the point of the last block, or the origin.
func (rd reader) Tip() ocommon.Point {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return follow(rd).tip(len(rd.w.blocks))
}

// Immutable returns how many blocks, counting from the first, can no longer
// be rolled back. A rollback to a height below it is ErrTooDeep.
func (rd reader) Immutable() int {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.immutable
}

// Blocks returns the current chain, oldest first.
func (rd reader) Blocks() []Block {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	out := make([]Block, len(rd.w.blocks))
	for i, r := range rd.w.blocks {
		out[i] = r.Block
	}
	return out
}

// Utxo returns the confirmed output at r.
func (rd reader) Utxo(r Ref) (common.Utxo, bool) {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	u, ok := rd.w.utxo[r]
	return u, ok
}

// AllUtxos returns the confirmed UTxO set sorted by reference.
func (rd reader) AllUtxos() []common.Utxo {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.utxo.sorted()
}

// UtxosAt returns the confirmed outputs paying addr, sorted by reference.
func (rd reader) UtxosAt(addr common.Address) []common.Utxo {
	want, err := addr.Bytes()
	if err != nil {
		return nil
	}
	var out []common.Utxo
	for _, u := range rd.AllUtxos() {
		got, err := u.Output.Address().Bytes()
		if err == nil && bytes.Equal(got, want) {
			out = append(out, u)
		}
	}
	return out
}

// UnspentLovelace returns the lovelace held in confirmed outputs.
func (rd reader) UnspentLovelace() uint64 {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	var sum uint64
	for _, u := range rd.w.utxo {
		sum += u.Output.Amount().Uint64()
	}
	return sum
}

// Fees returns the fees collected by the blocks on the current chain.
func (rd reader) Fees() uint64 {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.fees
}

// HasTx reports whether the mempool holds id, as local-tx-monitor's HasTx.
func (rd reader) HasTx(id TxID) bool {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.pool.find(id) != nil
}

// Mempool returns the mempool's transactions in order.
func (rd reader) Mempool() []TxID {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	out := make([]TxID, len(rd.w.pool.txs))
	for i, tx := range rd.w.pool.txs {
		out[i] = tx.Hash()
	}
	return out
}

// Evictions returns every transaction that left the mempool unincluded, in
// order. A node does not report these; tests use them as ground truth.
func (rd reader) Evictions() []Eviction {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return append([]Eviction(nil), rd.w.evicted...)
}

// Deposits returns the lovelace held in confirmed stake, pool, DRep and
// proposal deposits.
func (rd reader) Deposits() uint64 {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.certs.deposits()
}

// Treasury returns the treasury's lovelace and the lovelace in reward
// accounts, as of the confirmed tip.
func (rd reader) Treasury() (treasury, rewards uint64) {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.certs.treasury, held(rd.w.certs) - rd.w.certs.treasury
}

// Params returns the protocol parameters with every enacted parameter
// change applied, as a node's current-parameters query answers.
func (rd reader) Params() conway.ConwayProtocolParameters {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	return rd.w.certs.enact.params
}

// StakeRegistered reports whether cred is a confirmed registered stake
// credential.
func (rd reader) StakeRegistered(cred common.Credential) bool {
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	_, ok := rd.w.certs.accounts[keyOf(cred)]
	return ok
}

// Evaluate runs txCbor's Plutus scripts against the mempool's view of the
// ledger, each against the per-transaction budget rather than its declared
// units, and returns the units each redeemer used. It is the measurement
// the PlutusScripts rule compares declared units with.
func (rd reader) Evaluate(txCbor []byte) (map[common.RedeemerKey]common.ExUnits, error) {
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	rd.w.mu.Lock()
	defer rd.w.mu.Unlock()
	rd.w.view, rd.w.ls.certs = rd.w.pool.view.get, rd.w.pool.view.certs
	defer func() { rd.w.view, rd.w.ls.certs = nil, nil }()
	budget := rd.w.pp.MaxTxExUnits
	return conway.EvaluatePlutusScripts(tx, rd.w.ls, rd.w.pp, &budget)
}

// Ledger returns the state at the tip, in a fixed order.
func (rd reader) Ledger() Ledger {
	w := rd.w
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.certs
	l := Ledger{
		Label: s.epoch.label, Frozen: s.epoch.frozen,
		Mark: slices.Clone(s.epoch.mark), Set: slices.Clone(s.epoch.set),
	}
	for _, k := range sortedCreds(s.accounts) {
		a := s.accounts[k]
		acc := Account{Cred: k.hash}
		if a.pool != nil {
			p := common.Blake2b224(*a.pool)
			acc.Pool = &p
		}
		if a.drep != nil {
			d := *a.drep
			acc.DRep = &d
		}
		l.Accounts = append(l.Accounts, acc)
	}
	for id, p := range s.pools {
		l.Pools = append(l.Pools, PoolStatus{ID: common.Blake2b224(id), RetireAt: p.retire})
	}
	slices.SortFunc(l.Pools, func(a, b PoolStatus) int { return slices.Compare(a.ID[:], b.ID[:]) })
	for id := range s.epoch.retired {
		l.Retired = append(l.Retired, common.Blake2b224(id))
	}
	slices.SortFunc(l.Retired, func(a, b common.Blake2b224) int { return slices.Compare(a[:], b[:]) })
	for _, k := range sortedCreds(s.dreps) {
		l.DReps = append(l.DReps, k.hash)
	}
	for id, p := range s.proposals {
		l.Proposals = append(l.Proposals, ProposalStatus{ID: id, ExpirySlot: p.state.ExpirySlot})
	}
	slices.SortFunc(l.Proposals, func(a, b ProposalStatus) int {
		if c := slices.Compare(a.ID.TransactionId[:], b.ID.TransactionId[:]); c != 0 {
			return c
		}
		return int(a.ID.GovActionIdx) - int(b.ID.GovActionIdx)
	})
	for k, v := range s.votes {
		l.Votes = append(l.Votes, Vote{Action: k.action, Kind: k.kind, Hash: k.hash, Choice: v})
	}
	slices.SortFunc(l.Votes, func(a, b Vote) int {
		if c := slices.Compare(a.Action.TransactionId[:], b.Action.TransactionId[:]); c != 0 {
			return c
		}
		if c := int(a.Action.GovActionIdx) - int(b.Action.GovActionIdx); c != 0 {
			return c
		}
		if c := int(a.Kind) - int(b.Kind); c != 0 {
			return c
		}
		return slices.Compare(a.Hash[:], b.Hash[:])
	})
	return l
}
