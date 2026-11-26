package scenario

import "strings"

// Owner says which adversary a model family's traces belong to. Only the
// first three have an adapter in this package; the rest are recorded so a
// harvest says where each trace goes.
type Owner string

const (
	// OwnerTxCrafter: ledger rules a submitted transaction can reach.
	OwnerTxCrafter Owner = "tx-crafter (this package)"
	// OwnerMalformedTx: codec decisions, as malformed transactions.
	OwnerMalformedTx Owner = "malformed-tx (this package)"
	// OwnerRoguePeer: mini-protocol state machines, driven by a peer.
	OwnerRoguePeer Owner = "rogue peer (mini-protocols)"
	// OwnerRoguePool: consensus and block-level rules, driven by a forger.
	OwnerRoguePool Owner = "rogue pool (consensus, blocks)"
	// OwnerNone: internal interleavings or eras a devnet cannot reach.
	OwnerNone Owner = "none (not drivable from outside)"
	// OwnerLater: ledger rules a transaction reaches, with no adapter yet.
	OwnerLater Owner = "tx-crafter (no adapter yet)"
)

var owners = map[string]Owner{
	"ledger-utxo": OwnerTxCrafter, "ledger-witnesses": OwnerTxCrafter,
	"cbor-codec": OwnerMalformedTx, "ledger-codec": OwnerMalformedTx, "txsize-framing": OwnerMalformedTx,

	"blockfetch": OwnerRoguePeer, "chainsync": OwnerRoguePeer, "txsubmission": OwnerRoguePeer,
	"handshake": OwnerRoguePeer, "keepalive": OwnerRoguePeer, "peersharing": OwnerRoguePeer,
	"localtx": OwnerRoguePeer, "localstatequery": OwnerRoguePeer, "dmq": OwnerRoguePeer,
	"pipeline-cbor": OwnerRoguePeer, "chainsync-jumping": OwnerRoguePeer, "txsubmission-client": OwnerRoguePeer,

	"ledger-bbody": OwnerRoguePool, "ledger-block-overlay": OwnerRoguePool, "ledger-leios": OwnerRoguePool,
	"leios-peras": OwnerRoguePool,

	"connection": OwnerNone, "engine": OwnerNone, "ledger-hash-cache": OwnerNone,
	"metadata-migration-lock": OwnerNone, "hello": OwnerNone,
	"ledger-byron": OwnerNone, "ledger-shelley": OwnerNone, "ledger-ppup-mir": OwnerNone,
}

// OwnerOf returns the adversary that owns a model family's traces.
func OwnerOf(family string) Owner {
	if o, ok := owners[family]; ok {
		return o
	}
	if strings.HasPrefix(family, "consensus") {
		return OwnerRoguePool
	}
	return OwnerLater
}
