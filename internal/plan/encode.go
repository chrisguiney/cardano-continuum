package plan

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// BlobVersion is the version of the encoding a Committer receives, stored
// as store.Checkpoint.Reservations. Version 1 was the store's opaque
// placeholder, which carried no reservation state; Open refuses it.
const BlobVersion = 2

// disk is the durable part of the state: everything at and below the
// settled point, and every unfinished attempt. The blocks above the settled
// point are not stored; chain-sync replays them after ResumePoint.
type disk struct {
	SettledSlot uint64
	SettledHash []byte
	Utxos       []Utxo
	Attempts    []record
	Paid        []IntentID
	NextSeq     uint64
}

// encode returns the canonical encoding of s, sorted so equal states give
// equal bytes.
func encode(s *state) (store.Blob, error) {
	d := disk{SettledSlot: s.settled.Slot, NextSeq: s.nextSeq}
	if !s.settled.IsOrigin() {
		d.SettledHash = s.settled.Hash[:]
	}
	d.Utxos = slices.SortedFunc(maps.Values(s.utxos), func(a, b Utxo) int { return compareRefs(a.Ref, b.Ref) })
	for _, rec := range s.sorted() {
		d.Attempts = append(d.Attempts, *rec)
	}
	d.Paid = slices.Sorted(maps.Keys(s.paid))
	raw, err := eventlog.Encode(d)
	if err != nil {
		return store.Blob{}, fmt.Errorf("plan: encode: %w", err)
	}
	return store.Blob{Version: BlobVersion, Data: raw}, nil
}

// decode reads a stored state. The blob comes from disk, which another
// process can write, so every invariant a commit guarantees is checked.
func decode(k uint64, b store.Blob) (*state, error) {
	if b.Version != BlobVersion {
		return nil, fmt.Errorf("%w: version %d, want %d", ErrBlob, b.Version, BlobVersion)
	}
	var d disk
	if err := eventlog.Decode(b.Data, &d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlob, err)
	}
	s := newState(k)
	switch len(d.SettledHash) {
	case 0:
		if d.SettledSlot != 0 {
			return nil, fmt.Errorf("%w: settled slot %d has no hash", ErrBlob, d.SettledSlot)
		}
	case common.Blake2b256Size:
		s.settled = chain.Point{Slot: d.SettledSlot, Hash: common.Blake2b256(d.SettledHash)}
	default:
		return nil, fmt.Errorf("%w: settled hash of %d bytes", ErrBlob, len(d.SettledHash))
	}
	for _, u := range d.Utxos {
		if _, dup := s.utxos[u.Ref]; dup {
			return nil, fmt.Errorf("%w: output %x#%d twice", ErrBlob, u.Ref.Tx[:4], u.Ref.Index)
		}
		s.putUtxo(u)
	}
	for _, id := range d.Paid {
		s.paid[id] = struct{}{}
	}
	if err := restoreAttempts(s, d.Attempts, d.NextSeq); err != nil {
		return nil, err
	}
	if err := s.consistent(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlob, err)
	}
	return s, nil
}

// restoreAttempts adds the stored attempts recs to s, checking each against
// what Reserve would have accepted.
func restoreAttempts(s *state, recs []record, nextSeq uint64) error {
	if !slices.IsSortedFunc(recs, func(a, b record) int { return cmp.Compare(a.Seq, b.Seq) }) {
		return fmt.Errorf("%w: attempts out of order", ErrBlob)
	}
	var last uint64
	for i := range recs {
		rec := &recs[i]
		if rec.Seq <= last || rec.Seq > nextSeq {
			return fmt.Errorf("%w: attempt seq %d after %d, next %d", ErrBlob, rec.Seq, last, nextSeq)
		}
		last = rec.Seq
		if err := s.wellFormed(rec.Attempt); err != nil {
			return fmt.Errorf("%w: %w", ErrBlob, err)
		}
		if _, ok := s.paid[rec.Intent]; ok {
			return fmt.Errorf("%w: unfinished attempt of paid intent %d", ErrBlob, rec.Intent)
		}
		for _, r := range rec.Inputs {
			if h, ok := s.held[r]; ok && h != rec.Intent {
				return fmt.Errorf("%w: %x#%d held by intents %d and %d", ErrBlob, r.Tx[:4], r.Index, h, rec.Intent)
			}
			s.held[r] = rec.Intent
		}
		s.addRec(rec)
	}
	s.nextSeq = nextSeq
	return nil
}
