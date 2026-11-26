package forger

import (
	"slices"
	"testing"
)

func TestDrives(t *testing.T) {
	tests := []struct {
		family, config string
		want           Kind
	}{
		{"consensus", "ChainSelectionLowerIssueNoBug", Equivocate},
		{"consensus", "PBFTWindowNoPlusOneBug", ""},
		{"consensus-eras", "HeaderValidationProbeKESBeforeVRFBug", KESWrongPeriod},
		{"consensus-eras", "HeaderValidationOverlayBug", VRFBadProof},
		{"consensus-eras", "EraHistoryHorizonPlusOneBug", ""},
		{"consensus-nonce", "EpochNonceVrfInputBug", VRFBadProof},
		{"consensus-genesis", "GenesisDensityNoKGuardBug", ""},
		{"consensus-tachys", "TachysSkipVrfBug", ""},
		{"ledger-bbody", "BlockBodyNoHashCheckBug", BodyHash},
		{"ledger-bbody", "BlockBodyNoSizeCheckBug", Oversize},
		{"ledger-bbody", "BlockBodyProbeChainedSpendBug", ""},
		{"ledger-block-overlay", "GovOverlayReuseBug", ""},
	}
	for _, tt := range tests {
		got, ok := Drives(tt.family, tt.config)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("Drives(%s, %s) = %q, %v; want %q", tt.family, tt.config, got, ok, tt.want)
		}
	}
	for _, r := range route {
		if !slices.Contains(Kinds(), r.kind) {
			t.Errorf("route %s/%s names %q, which is not a misbehaviour", r.family, r.prefix, r.kind)
		}
	}
}
