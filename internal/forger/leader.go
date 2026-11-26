package forger

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/consensus"
)

// vrfSigner adapts Credentials to consensus.VRFSigner.
type vrfSigner struct{ c *Credentials }

func (s vrfSigner) Prove(alpha []byte) ([]byte, []byte, error) { return s.c.vrfProve(alpha) }
func (s vrfSigner) PublicKey() []byte                          { return s.c.VRFVKey }

// Leader reports whether the pool leads slot under epochNonce: its VRF
// output for the slot is below the threshold of its stake share
// (consensus.IsSlotLeader, the Praos checkLeaderNatValue).
func Leader(p Params, c *Credentials, epochNonce [32]byte, slot uint64) (bool, error) {
	res, err := consensus.IsSlotLeader(slot, epochNonce[:], p.PoolStake, p.TotalStake, p.ActiveSlots, vrfSigner{c})
	if err != nil {
		return false, fmt.Errorf("leader check for slot %d: %w", slot, err)
	}
	return res.Eligible, nil
}
