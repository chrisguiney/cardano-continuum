package fakechain

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// txpool is the mempool's behaviour over the world. Its methods assume the
// caller holds world.mu.
type txpool struct{ w *world }

// submit validates tx against the ledger and the mempool and adds it.
func (p txpool) submit(tx *conway.ConwayTransaction) error {
	w := p.w
	size := uint64(len(tx.Cbor()))
	if w.pool.bytes+size > w.capBytes {
		return fmt.Errorf("%w: %d + %d bytes over %d", ErrMempoolFull, w.pool.bytes, size, w.capBytes)
	}
	slot := p.slot()
	fs := unelected(tx, w.pool.view.certs)
	if more := w.check(tx, slot, w.pool.view); len(more) > 0 {
		fs = append(fs, more...)
	}
	if len(fs) > 0 {
		return &RejectError{Tx: tx.Hash(), Failures: fs}
	}
	w.pool.push(tx, slot, effectsOf(w))
	return nil
}

// unelected is the MEMPOOL rule's committee check: a vote by a committee
// hot credential that no current, unresigned member has authorised is
// refused, judged on s, the state before tx. That is stricter than the
// ledger rule a block is held to: the formal spec's LEDGER rule hands GOVS
// the certificate state after the transaction's own certificates
// (formal-ledger-specifications f8059737f1db, src/Ledger/Conway/
// Specification/Ledger.lagda.md, LEDGER; Gov.lagda.md, GOV-Vote and
// isRegistered; the authorisation is Certs.lagda.md, GOVCERT-ccreghot). So
// a vote carrying its own hot-key authorisation is valid in a block but
// never enters a cardano-node mempool (cardano-ledger 085b8cc6b17c,
// eras/conway/impl/src/Cardano/Ledger/Conway/Rules/Mempool.hs,
// mempoolTransition and unelectedCommitteeVoters). From protocol version
// 11 the ledger moves the check into GOV; the fake applies it at every
// version.
func unelected(tx *conway.ConwayTransaction, s *certState) []Failure {
	hot := map[credKey]bool{}
	for _, m := range s.committee {
		if m.hot != nil && !m.resigned {
			hot[*m.hot] = true
		}
	}

	var bad []string
	for v := range tx.VotingProcedures() {
		k, ok := committeeVoter(v)
		if ok && !hot[k] {
			bad = append(bad, hex.EncodeToString(v.Hash[:]))
		}
	}

	if len(bad) == 0 {
		return nil
	}
	slices.Sort(bad)
	return []Failure{{
		Class: MempoolFailure,
		Rule:  "mempool",
		Err:   fmt.Errorf("unelected committee members are not allowed to cast votes: %s", strings.Join(bad, ", ")),
	}}
}

// committeeVoter is the hot credential of a committee voter.
func committeeVoter(v *common.Voter) (credKey, bool) {
	switch v.Type {
	case common.VoterTypeConstitutionalCommitteeHotKeyHash:
		return credKey{common.CredentialTypeAddrKeyHash, v.Hash}, true
	case common.VoterTypeConstitutionalCommitteeHotScriptHash:
		return credKey{common.CredentialTypeScriptHash, v.Hash}, true
	}
	return credKey{}, false
}

// slot is the slot a mempool validates at: the one after the tip, as
// the node's mempool ticks its ledger state.
func (p txpool) slot() uint64 {
	if len(p.w.blocks) == 0 {
		return 0
	}
	return p.w.blocks[len(p.w.blocks)-1].Slot + 1
}

func (p txpool) evict(tx *conway.ConwayTransaction, why Reason, fs []Failure) {
	w := p.w
	w.pool.remove([]*conway.ConwayTransaction{tx})
	w.evicted = append(w.evicted, Eviction{Tx: tx.Hash(), Slot: w.slot, Reason: why, Failures: fs})
}

// revalidate rebuilds the mempool against the current tip, in order,
// evicting what no longer applies.
func (p txpool) revalidate() {
	w := p.w
	old := w.pool.txs
	w.pool = newMempool(w.utxo, w.certs)
	slot := p.slot()
	r := effectsOf(w)
	for _, tx := range old {
		if fs := w.check(tx, slot, w.pool.view); len(fs) > 0 {
			w.evicted = append(w.evicted, Eviction{Tx: tx.Hash(), Slot: w.slot, Reason: ReasonInvalid, Failures: fs})
			continue
		}
		w.pool.push(tx, slot, r)
	}
}
