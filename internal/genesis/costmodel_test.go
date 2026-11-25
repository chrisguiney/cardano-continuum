package genesis

import (
	"testing"

	"github.com/blinklabs-io/plutigo/lang"
)

// The embedded model predates the builtins plutigo knows about past index
// 250. plutigo costs every missing parameter at maxBound, so those builtins
// cannot run within any budget; nothing the builder deploys uses them. A
// longer model than plutigo's name list would be truncated silently.
func TestPlutusV3CostModel(t *testing.T) {
	m := PlutusV3CostModel()
	if len(m) != PlutusV3CostModelLen {
		t.Fatalf("cost model has %d entries, want %d", len(m), PlutusV3CostModelLen)
	}
	if names := len(lang.GetParamNamesForVersion(lang.LanguageVersionV3)); len(m) > names {
		t.Fatalf("cost model has %d entries, plutigo names only %d: the rest would be dropped", len(m), names)
	}
	m[0] = -1
	if PlutusV3CostModel()[0] == -1 {
		t.Fatal("PlutusV3CostModel returns shared storage")
	}
}
