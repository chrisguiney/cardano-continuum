package genesis

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// renderByron returns a Byron genesis already in canonical form, so its
// file bytes and the bytes cardano-node hashes are the same. It has no boot
// stakeholders or heavy delegation: every hard fork is at epoch 0, so no
// Byron block is ever made. Byron's k must equal Shelley's, which the
// hard-fork combinator assumes when it crosses the era boundary.
//
// The block version data are cardano-cli 11.2.3.1's create-testnet-data
// defaults, with slotDuration set from the Spec.
func renderByron(s Spec) ([]byte, error) {
	doc := map[string]any{
		"avvmDistr":        map[string]any{},
		"bootStakeholders": map[string]any{},
		"heavyDelegation":  map[string]any{},
		"nonAvvmBalances":  map[string]any{},
		"protocolConsts": map[string]any{
			"k":             s.SecurityParam,
			"protocolMagic": s.Magic,
		},
		"startTime": s.SystemStart.Unix(),
		"blockVersionData": map[string]any{
			"heavyDelThd":       "300000000000",
			"maxBlockSize":      "2000000",
			"maxHeaderSize":     "2000000",
			"maxProposalSize":   "700",
			"maxTxSize":         "4096",
			"mpcThd":            "20000000000000",
			"scriptVersion":     0,
			"slotDuration":      strconv.FormatInt(s.SlotLength.Milliseconds(), 10),
			"unlockStakeEpoch":  "18446744073709551615",
			"updateImplicit":    "10000",
			"updateProposalThd": "100000000000000",
			"updateVoteThd":     "1000000000000",
			"softforkRule": map[string]any{
				"initThd":      "900000000000000",
				"minThd":       "600000000000000",
				"thdDecrement": "50000000000000",
			},
			"txFeePolicy": map[string]any{
				"multiplier": "43946000000",
				"summand":    "155381000000000",
			},
		},
	}
	return canonical(doc)
}

// ByronHash is the hash cardano-node checks for a Byron genesis: Blake2b-256
// of the document's canonical JSON (keys sorted, no whitespace), not of the
// file bytes. It is the reference implementation's
// Cardano.Chain.Genesis.Hash over Text.JSON.Canonical rendering. Only the
// subset of canonical JSON a Byron genesis uses is supported: integers,
// strings, objects and arrays.
func ByronHash(file []byte) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(file))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("parse byron genesis: %w", err)
	}
	c, err := canonical(v)
	if err != nil {
		return "", err
	}
	h := common.Blake2b256Hash(c)
	return hex.EncodeToString(h[:]), nil
}

// canonical renders v as canonical JSON. encoding/json already sorts map keys
// by byte order and writes no whitespace; HTML escaping is the only
// difference to switch off. Canonical JSON has no fractions or exponents, so
// any non-integer number is an error rather than a silent hash mismatch.
func canonical(v any) ([]byte, error) {
	if err := integersOnly(v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("render canonical json: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func integersOnly(v any) error {
	switch t := v.(type) {
	case map[string]any:
		for _, e := range t {
			if err := integersOnly(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range t {
			if err := integersOnly(e); err != nil {
				return err
			}
		}
	case json.Number:
		if _, err := strconv.ParseInt(t.String(), 10, 64); err != nil {
			return fmt.Errorf("canonical json: %s is not an integer", t)
		}
	case float32, float64:
		return fmt.Errorf("canonical json: float %v", t)
	}
	return nil
}
