package forger

import "strings"

// route maps a TLA+ bug config of the model repository (by family and
// config-name prefix) to the misbehaviour that drives a node into the state
// its trace ends in. `continuum scenario harvest` routes these families to
// the rogue pool (scenario.OwnerRoguePool).
var route = []struct {
	family, prefix string
	kind           Kind
}{
	// Two candidates for one slot from one issuer: the tie-break rules.
	{"consensus", "ChainSelection", Equivocate},
	// The order header checks run in, and what each one refuses.
	{"consensus-eras", "HeaderValidationProbeKES", KESWrongPeriod},
	{"consensus-eras", "HeaderValidation", VRFBadProof},
	// A proof made under another nonce or input than the epoch's.
	{"consensus-nonce", "EpochNonce", VRFBadProof},
	{"ledger-bbody", "BlockBodyNoHashCheck", BodyHash},
	{"ledger-bbody", "BlockBodyNoSizeCheck", Oversize},
	{"ledger-bbody", "BlockBodyGoZeroMaxBody", Oversize},
}

// Drives is the misbehaviour that exercises the rule a harvested trace is
// about, and false for a trace no forged block reaches. Not drivable: the
// Byron and PBFT models (the devnet starts in Conway), era history, Genesis
// density (it needs a syncing node, not a block), Tachys, Leios and Peras
// (experimental), the block-body traces about transactions inside a block
// and its protocol version, and the governance overlay (the forger builds
// no valid transaction).
func Drives(family, config string) (Kind, bool) {
	for _, r := range route {
		if r.family == family && strings.HasPrefix(config, r.prefix) {
			return r.kind, true
		}
	}
	return "", false
}
