package forger

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// nonce is a Praos nonce; the zero value is NeutralNonce, the identity of
// combine.
type nonce struct {
	set  bool
	hash [32]byte
}

func hashNonce(h [32]byte) nonce { return nonce{set: true, hash: h} }

// combine is the ⭒ operator of cardano-ledger's Cardano/Ledger/BaseTypes.hs:
// the hash of both operands, or the other operand when one is neutral.
func combine(a, b nonce) nonce {
	if !a.set {
		return b
	}
	if !b.set {
		return a
	}
	var buf [64]byte
	copy(buf[:32], a.hash[:])
	copy(buf[32:], b.hash[:])
	return hashNonce(common.Blake2b256Hash(buf[:]))
}

// Nonces is the nonce part of the Praos chain-dependent state after some
// block, as ouroboros-consensus keeps it
// (Ouroboros/Consensus/Protocol/Praos.hs, PraosState).
type Nonces struct {
	// Epoch is the epoch of the last tick or block.
	Epoch uint64
	// epoch is the nonce leadership in Epoch is drawn with.
	epoch, evolving, candidate nonce
	// lab is the previous-hash nonce of the last block; lastEpochLab is its
	// value when the epoch began.
	lab, lastEpochLab nonce
}

// GenesisNonces is the state before the first block: every running nonce is
// the Shelley genesis hash.
func GenesisNonces(p Params) Nonces {
	g := hashNonce(p.GenesisHash)
	return Nonces{epoch: g, evolving: g, candidate: g}
}

// EpochNonce is the nonce of n.Epoch.
func (n Nonces) EpochNonce() [32]byte { return n.epoch.hash }

// Tick moves the state to slot without a block (tickChainDepState). Crossing
// any number of epoch boundaries is one transition: the new epoch nonce is
// the candidate combined with the previous-hash nonce held from the epoch
// before.
func (n Nonces) Tick(p Params, slot uint64) Nonces {
	e := p.Epoch(slot)
	if e <= n.Epoch {
		return n
	}
	n.epoch = combine(n.candidate, n.lastEpochLab)
	n.lastEpochLab = n.lab
	n.Epoch = e
	return n
}

// Apply is the state after a block at slot with the given previous hash
// (zero at genesis) and leader VRF output (reupdateChainDepState).
func (n Nonces) Apply(p Params, slot uint64, prev [32]byte, vrfOutput []byte) Nonces {
	n = n.Tick(p, slot)
	// vrfNonceValue: the range-extended output "N"||output, hashed once to
	// tag it and once more to make the nonce.
	tagged := common.Blake2b256Hash(append([]byte("N"), vrfOutput...))
	eta := hashNonce(common.Blake2b256Hash(tagged[:]))
	n.evolving = combine(n.evolving, eta)
	nextEpoch := (n.Epoch + 1) * p.EpochLength
	if slot+p.stabilityWindow() < nextEpoch {
		n.candidate = n.evolving
	}
	n.lab = nonce{}
	if prev != ([32]byte{}) {
		n.lab = hashNonce(prev)
	}
	return n
}
