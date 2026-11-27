package wire

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Digest reads a Conway block into BlockContent and encodes it. It is the
// I/O-side half of a BlockApplied input: the engine sees only the digest.
func Digest(blockCbor []byte) ([]byte, error) {
	blk, err := conway.NewConwayBlockFromCbor(blockCbor)
	if err != nil {
		return nil, fmt.Errorf("engine: decode block: %w", err)
	}
	return DigestOf(chain.BlockFrom(blk))
}

// DigestOf encodes a block as chain-sync delivered it. Consumed, Produced
// and the fee already follow the isValid rule (chain.TxFrom).
func DigestOf(b chain.Block) ([]byte, error) {
	c := BlockContent{Number: b.Number, Raw: b.Raw}
	for _, tx := range b.Txs {
		d := TxDigest{ID: TxID(tx.Hash), Fee: tx.Fee, Proposals: uint32(len(tx.Proposals))}
		for _, p := range tx.Proposals {
			d.Gov = append(d.Gov, ProposalDigest{Type: p.Type, Pools: p.Pools, Deposit: p.Deposit})
		}
		if u := tx.Units; u.Memory != 0 || u.Steps != 0 {
			d.Units = &Units{
				Memory: u.Memory,
				Steps:  u.Steps,
			}
		}
		for _, in := range tx.Spends {
			d.Inputs = append(d.Inputs, Ref{Tx: TxID(in.Tx), Index: in.Index})
		}
		for _, u := range tx.Creates {
			addr, err := u.Output.Address().Bytes()
			if err != nil {
				return nil, fmt.Errorf("engine: output address: %w", err)
			}
			d.Outputs = append(d.Outputs, TxOut{Index: u.Id.Index(), Address: addr, Lovelace: u.Output.Amount().Uint64(), Cbor: u.Output.Cbor()})
		}
		for _, cert := range tx.Certs {
			raw := cert.Cbor()
			if len(raw) == 0 {
				var err error
				if raw, err = cbor.Encode(cert); err != nil {
					return nil, fmt.Errorf("engine: encode certificate: %w", err)
				}
			}
			d.Certs = append(d.Certs, raw)
		}
		for _, v := range tx.Votes {
			d.Votes = append(d.Votes, VoteDigest{
				Kind: v.Voter.Kind, Hash: v.Voter.Hash, Tx: TxID(v.Action.Tx), Index: v.Action.Index, Choice: v.Choice,
			})
		}
		c.Txs = append(c.Txs, d)
	}
	return eventlog.Encode(c)
}
