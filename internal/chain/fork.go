package chain

import (
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// Fork is a hard fork the view's ratification accepted: the proposal, the
// version it moves to and the epoch whose boundary enacts it, the one
// after the boundary that ratified it (formal-ledger-specifications
// f8059737f1db, src/Ledger/Conway/Specification/Epoch.lagda.md: EPOCH
// applies the enact state the boundary before ratified).
type Fork struct {
	// ID is the HardForkInitiation proposal.
	ID GovID
	// Version is the protocol version it moves to.
	Version protocol.Version
	// At is the first epoch at Version.
	At uint64
}

// forks is the view's protocol-version history: the version at genesis
// and every hard fork ratified since, oldest first. The version in force
// in an epoch follows from them, so a rollback that takes a ratification
// back takes the version with it.
type forks struct {
	// base is the protocol version at genesis (Config.Version).
	base protocol.Version
	// list is every ratified hard fork, oldest first; each names the one
	// before it (prevGovActionId), the first none.
	list []Fork
}

// VersionAt is the protocol version in force in epoch ep, as far as the
// view has ratified: past the tip's epoch it may still change.
func (f *forks) VersionAt(ep uint64) protocol.Version {
	v := f.base
	for _, x := range f.list {
		if x.At <= ep {
			v = x.Version
		}
	}
	return v
}

// PendingAt is the hard fork ratified by epoch ep and enacted after it.
func (f *forks) PendingAt(ep uint64) (Fork, bool) {
	if n := len(f.list); n > 0 && f.list[n-1].At > ep {
		return f.list[n-1], true
	}
	return Fork{}, false
}

// Forks lists every ratified hard fork, oldest first.
func (f *forks) Forks() []Fork { return slices.Clone(f.list) }

// forkRoot is the hard-fork purpose root: the last ratified hard fork, nil
// while none has been.
func (f *forks) forkRoot() *GovID {
	if n := len(f.list); n > 0 {
		id := f.list[n-1].ID
		return &id
	}
	return nil
}

// forkOf reads a hard fork's predecessor and version; any other action has
// neither.
func forkOf(a common.GovAction) (*GovID, *protocol.Version) {
	hf, ok := a.(*common.HardForkInitiationGovAction)
	if !ok {
		return nil, nil
	}

	var prev *GovID
	if id := hf.ActionId; id != nil {
		prev = &GovID{Tx: id.TransactionId, Index: id.GovActionIdx}
	}
	return prev, &protocol.Version{Major: hf.ProtocolVersion.Major, Minor: hf.ProtocolVersion.Minor}
}
