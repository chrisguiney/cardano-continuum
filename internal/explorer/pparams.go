package explorer

import (
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// paramInfo describes one protocol parameter: its group is the CIP-1694
// grouping that sets which DRep threshold an update must clear
// (cardano-ledger eras/conway/impl/src/Cardano/Ledger/Conway/PParams.hs,
// the 'NetworkGroup, 'EconomicGroup, 'TechnicalGroup and 'GovGroup
// annotations of ConwayPParams).
type paramInfo struct {
	group, unit, desc string
}

// paramCatalog is keyed by the field name of conway.ConwayProtocolParameterUpdate.
var paramCatalog = map[string]paramInfo{
	"MinFeeA":                    {"economic", "lovelace per byte", "The fee every transaction pays for each byte of its size."},
	"MinFeeB":                    {"economic", "lovelace", "The flat fee every transaction pays, whatever its size."},
	"MaxBlockBodySize":           {"network", "bytes", "The most bytes the transactions of one block may take."},
	"MaxTxSize":                  {"network", "bytes", "The most bytes one transaction may take."},
	"MaxBlockHeaderSize":         {"network", "bytes", "The most bytes a block header may take."},
	"KeyDeposit":                 {"economic", "lovelace", "The deposit a stake key pays to register, returned when it deregisters."},
	"PoolDeposit":                {"economic", "lovelace", "The deposit a stake pool pays to register, returned when it retires."},
	"MaxEpoch":                   {"technical", "epochs", "How many epochs ahead a pool may announce its retirement."},
	"NOpt":                       {"technical", "pools", "The target number of stake pools, which sets the saturation point of a pool."},
	"A0":                         {"technical", "ratio", "How much a pool's pledge raises its rewards (the pledge influence)."},
	"Rho":                        {"economic", "ratio", "The share of the reserves paid out as rewards each epoch (monetary expansion)."},
	"Tau":                        {"economic", "ratio", "The share of each epoch's rewards that goes to the treasury."},
	"ProtocolVersion":            {"", "version", "The protocol version; a parameter change cannot set it, a hard fork does."},
	"MinPoolCost":                {"economic", "lovelace", "The least a pool may charge per epoch as its fixed fee."},
	"AdaPerUtxoByte":             {"economic", "lovelace per byte", "The deposit an output must hold for each byte it takes (the minimum UTxO)."},
	"CostModels":                 {"technical", "cost coefficients", "The coefficients that price each Plutus builtin in execution units."},
	"ExecutionCosts":             {"economic", "lovelace per unit", "What each memory unit and each CPU step of script execution costs in fees."},
	"MaxTxExUnits":               {"network", "memory units and CPU steps", "The most memory and CPU steps one transaction's scripts may use."},
	"MaxBlockExUnits":            {"network", "memory units and CPU steps", "The most memory and CPU steps all the scripts of one block may use."},
	"MaxValueSize":               {"network", "bytes", "The most bytes the value of one output (its ADA and native assets) may take."},
	"CollateralPercentage":       {"technical", "percent", "The collateral a script transaction must put up, as a percent of its fee."},
	"MaxCollateralInputs":        {"network", "inputs", "The most inputs a transaction may name as collateral."},
	"PoolVotingThresholds":       {"governance", "ratios", "The share of stake pool stake that must vote yes on each kind of action they vote on."},
	"DRepVotingThresholds":       {"governance", "ratios", "The share of DRep stake that must vote yes on each kind of action."},
	"MinCommitteeSize":           {"governance", "members", "The fewest active constitutional committee members for the committee to ratify."},
	"CommitteeTermLimit":         {"governance", "epochs", "The longest term a committee member may be given."},
	"GovActionValidityPeriod":    {"governance", "epochs", "How many epochs a governance action stays open for votes (GovLifetime)."},
	"GovActionDeposit":           {"governance", "lovelace", "The deposit a governance action pays, returned when it ends."},
	"DRepDeposit":                {"governance", "lovelace", "The deposit a DRep pays to register, returned when it retires."},
	"DRepInactivityPeriod":       {"governance", "epochs", "How many epochs a DRep may go without voting before it counts as inactive."},
	"MinFeeRefScriptCostPerByte": {"economic", "lovelace per byte", "The fee for each byte of reference script a transaction pulls in."},
}

// ParamChange is one protocol parameter a parameter-change action sets.
// Current is empty when the parameter's present value is not known.
type ParamChange struct {
	// Field is the gouroboros field name, Name the camelCase name a reader
	// knows it by.
	Field string `json:"field"`
	Name  string `json:"name"`
	// Group is network, economic, technical or governance; empty for a
	// parameter outside the groups.
	Group string `json:"group"`
	// Security reports membership in the security group, which makes stake
	// pools vote on the action (see chain.actionOf).
	Security bool   `json:"security"`
	Unit     string `json:"unit"`
	Desc     string `json:"desc"`
	New      string `json:"new"`
	Current  string `json:"current,omitempty"`

	// newB and curB are the values as Summary writes them, sfx its unit.
	newB, curB, sfx string
}

// paramChanges lists the parameters upd sets, in the update's field order.
// security names the fields the ledger puts in the security group
// (common.ParameterChangeGovAction.SecurityGroupFields).
func paramChanges(upd reflect.Value, security []string) []ParamChange {
	var out []ParamChange
	for i := range upd.NumField() {
		f, sf := upd.Field(i), upd.Type().Field(i)
		if !sf.IsExported() || sf.Anonymous || isZeroSetting(f) {
			continue
		}
		info := paramCatalog[sf.Name]
		nb, sfx := brief(f)
		out = append(out, ParamChange{
			Field: sf.Name, Name: lowerFirst(sf.Name), Group: info.group, Security: slices.Contains(security, sf.Name),
			Unit: info.unit, Desc: info.desc, New: display(f), newB: nb, sfx: sfx,
		})
	}
	return out
}

// isZeroSetting reports an update field the action leaves unset.
func isZeroSetting(f reflect.Value) bool {
	switch f.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		return f.IsNil()
	}
	return false
}

// setCurrent fills each change's Current from the same-named field of cur,
// a struct (or pointer to one) of protocol parameters.
func setCurrent(ps []ParamChange, cur reflect.Value) []ParamChange {
	for cur.Kind() == reflect.Pointer && !cur.IsNil() {
		cur = cur.Elem()
	}
	if cur.Kind() != reflect.Struct {
		return ps
	}
	out := slices.Clone(ps)
	for i := range out {
		f := cur.FieldByName(out[i].Field)
		if !f.IsValid() || isZeroSetting(f) {
			continue
		}
		out[i].Current = display(f)
		out[i].curB, _ = brief(f)
	}
	return out
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

var ratType = reflect.TypeFor[cbor.Rat]()
var exUnitsType = reflect.TypeFor[common.ExUnits]()
var versionType = reflect.TypeFor[common.ProtocolParametersProtocolVersion]()

var languages = map[uint]string{0: "PlutusV1", 1: "PlutusV2", 2: "PlutusV3"}

// display renders a protocol parameter value for a reader: integers with
// digit groups, ratios as decimals, structures field by field.
func display(v reflect.Value) string {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch {
	case v.Type() == ratType:
		return ratText(v.Interface().(cbor.Rat).Rat)
	case v.Type() == exUnitsType:
		e := v.Interface().(common.ExUnits)
		return groupedInt(e.Memory) + " mem, " + groupedInt(e.Steps) + " steps"
	case v.Type() == versionType:
		pv := v.Interface().(common.ProtocolParametersProtocolVersion)
		return fmt.Sprintf("%d.%d", pv.Major, pv.Minor)
	}
	switch v.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return grouped(v.Uint())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return groupedInt(v.Int())
	case reflect.Map:
		return costModels(v)
	case reflect.Struct:
		return fields(v)
	}
	return fmt.Sprint(v.Interface())
}

func fields(v reflect.Value) string {
	var parts []string
	for i := range v.NumField() {
		sf := v.Type().Field(i)
		if !sf.IsExported() || sf.Anonymous {
			continue
		}
		parts = append(parts, lowerFirst(sf.Name)+" "+display(v.Field(i)))
	}
	return strings.Join(parts, ", ")
}

// costModels names each language and counts its coefficients; the values
// themselves are hundreds of numbers a reader cannot compare.
func costModels(v reflect.Value) string {
	m, ok := v.Interface().(map[uint][]int64)
	if !ok {
		return fmt.Sprint(v.Interface())
	}
	keys := make([]uint, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name, ok := languages[k]
		if !ok {
			name = "language " + strconv.FormatUint(uint64(k), 10)
		}
		parts = append(parts, fmt.Sprintf("%s: %d values", name, len(m[k])))
	}
	return strings.Join(parts, ", ")
}

// ratText writes a ratio as a decimal, to six places without trailing zeros.
func ratText(r *big.Rat) string {
	if r == nil {
		return ""
	}
	if r.IsInt() {
		return r.Num().String()
	}
	s := strings.TrimRight(r.FloatString(6), "0")
	return strings.TrimSuffix(s, ".")
}

// grouped writes n with a comma between thousands.
func grouped(n uint64) string {
	s := strconv.FormatUint(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// groupedInt is grouped for a value that may be negative.
func groupedInt(n int64) string {
	if n < 0 {
		return "-" + grouped(uint64(-(n+1))+1)
	}
	return grouped(uint64(n))
}

// brief is a value as Summary writes it: a count of a million or more is
// abbreviated, an execution budget shows its memory with sfx " mem".
func brief(v reflect.Value) (val, sfx string) {
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() == reflect.Pointer {
		return "", ""
	}
	if v.Type() == exUnitsType {
		return abbrev(uint64(max(v.Interface().(common.ExUnits).Memory, 0))), " mem"
	}
	switch v.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return abbrev(v.Uint()), ""
	}
	return display(v), ""
}

// abbrev writes 16500000 as 16.5M and leaves anything under a million whole.
func abbrev(n uint64) string {
	for _, u := range []struct {
		div    uint64
		suffix string
	}{{1_000_000_000, "B"}, {1_000_000, "M"}} {
		if n >= u.div {
			s := strconv.FormatFloat(float64(n)/float64(u.div), 'f', 1, 64)
			return strings.TrimSuffix(s, ".0") + u.suffix
		}
	}
	return grouped(n)
}
