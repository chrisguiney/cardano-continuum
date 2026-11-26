package genesis

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ExUnits is a Plutus execution budget.
type ExUnits struct {
	Memory uint64 `json:"memory"`
	Steps  uint64 `json:"steps"`
}

// Fits reports whether u is within limit on both dimensions.
func (u ExUnits) Fits(limit ExUnits) bool { return u.Memory <= limit.Memory && u.Steps <= limit.Steps }

// Mainnet's execution limits in force at epoch 669 (param_update: block
// steps lowered to 20B at epoch 393, then the Conway parameter change in
// c21b00f90f18fce4... raising tx memory to 16.5M and block memory to 72M,
// observed in force from epoch 614; no block or transaction since exceeds
// them). cardano-api's alonzoGenesisDefaults, which cardano-cli
// create-testnet-data writes, has a per-transaction memory of 140M against
// a per-block 62M (cardano-api Cardano/Api/Genesis/Internal.hs at 1a6ad259):
// a transaction that can never be forged passes the per-transaction check.
var (
	mainnetTxExUnits    = ExUnits{Memory: 16_500_000, Steps: 10_000_000_000}
	mainnetBlockExUnits = ExUnits{Memory: 72_000_000, Steps: 20_000_000_000}
)

// Allow names the inconsistencies a devnet carries on purpose, so Check
// does not refuse them.
type Allow struct {
	// InconsistentExUnits lets the per-transaction budget exceed the
	// per-block one, opening the gap the exunits-block-gap attack tests:
	// a transaction the mempool admits but no block can hold.
	InconsistentExUnits bool
	// ShortCostModels lets CostModelLen drop parameters the protocol
	// version defines, making the builtins they price unusable.
	ShortCostModels bool
}

// checkLimits refuses an empty budget, and a per-transaction budget above
// the per-block one unless Allow says it is deliberate.
func checkLimits(s Spec) error {
	tx, blk := s.TxExUnits, s.BlockExUnits
	if tx.Memory == 0 || tx.Steps == 0 || blk.Memory == 0 || blk.Steps == 0 {
		return fmt.Errorf("maxTxExUnits %+v and maxBlockExUnits %+v must be positive", tx, blk)
	}
	if !tx.Fits(blk) && !s.Allow.InconsistentExUnits {
		return fmt.Errorf("maxTxExUnits %+v exceeds maxBlockExUnits %+v: such a transaction is admitted but never forged; allow inconsistent ExUnits to run it anyway", tx, blk)
	}
	return nil
}

// Override is one validated `path=value` change to the default Spec, as
// continuum-genesis -override takes it.
type Override struct {
	path  string
	value uint64
}

// overridable maps each path an Override may name to the Spec field it
// sets. The paths are the genesis files' own key names.
var overridable = map[string]func(*Spec, uint64){
	"maxTxExUnits.memory":        func(s *Spec, v uint64) { s.TxExUnits.Memory = v },
	"maxTxExUnits.steps":         func(s *Spec, v uint64) { s.TxExUnits.Steps = v },
	"maxBlockExUnits.memory":     func(s *Spec, v uint64) { s.BlockExUnits.Memory = v },
	"maxBlockExUnits.steps":      func(s *Spec, v uint64) { s.BlockExUnits.Steps = v },
	"costModels.PlutusV1.length": func(s *Spec, v uint64) { s.CostModelLen[PlutusV1] = int(v) },
	"costModels.PlutusV2.length": func(s *Spec, v uint64) { s.CostModelLen[PlutusV2] = int(v) },
	"costModels.PlutusV3.length": func(s *Spec, v uint64) { s.CostModelLen[PlutusV3] = int(v) },
}

// OverridePaths returns every path an Override may name, sorted.
func OverridePaths() []string {
	out := make([]string, 0, len(overridable))
	for p := range overridable {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// maxOverride bounds a value so it fits every field it can set, int
// included.
const maxOverride = 1 << 62

// ParseOverride parses `path=value`, a path from OverridePaths and a
// decimal value.
func ParseOverride(s string) (Override, error) {
	path, raw, ok := strings.Cut(s, "=")
	if !ok {
		return Override{}, errors.New("override must be path=value")
	}
	if _, known := overridable[path]; !known {
		return Override{}, fmt.Errorf("override path %q: want one of %s", path, strings.Join(OverridePaths(), ", "))
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v > maxOverride {
		return Override{}, fmt.Errorf("override %s: value %q is not a decimal integer up to 2^62", path, raw)
	}
	return Override{path: path, value: v}, nil
}

// Apply sets the field o names. Check still judges the result.
func (o Override) Apply(s *Spec) { overridable[o.path](s, o.value) }

func (o Override) String() string { return o.path + "=" + strconv.FormatUint(o.value, 10) }
