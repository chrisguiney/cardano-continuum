package scenario

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// ttlBugCfg is UtxoGoTtlExclusiveBug.cfg of the model repository.
const ttlBugCfg = `\* SPEC: Utxo.tla
SPECIFICATION TableSpec
CONSTANTS
    Era = "Shelley"
    MinFee = 1
    MinUtxo = 1
    KeyDeposit = 1
    CollPct = 150
    MaxColl = 2
    StartUtxo <- LedgerStart
    MaxTxs = 0
    Mutation = "GoTtlExclusive"
INVARIANTS
    GoAgrees
`

// TestScenarioGolden: harvesting UtxoGoTtlExclusiveBug.cfg yields the
// committed scenario, provenance included.
func TestScenarioGolden(t *testing.T) {
	t.Parallel()
	jar, _ := filepath.Abs("../../models/.tools/tla2tools-1.8.0.jar")
	if _, err := os.Stat(jar); err != nil {
		t.Skipf("TLC 1.8.0 jar not installed (run make models-install): %v", err)
	}
	tlc, _ := filepath.Abs("../../models/bin/tlc")
	dir := t.TempDir()
	model, err := os.ReadFile("../tla/testdata/Utxo.tla")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"Utxo.tla": string(model), "UtxoGoTtlExclusiveBug.cfg": ttlBugCfg} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ex, err := tla.ExportUtxo(t.Context(), tla.Tools{TLC: tlc, Jar: jar, Tmp: t.TempDir()}, dir, "UtxoGoTtlExclusiveBug.cfg", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Find("../../scenarios", "ledger-utxo/UtxoGoTtlExclusiveBug/0")
	if err != nil {
		t.Fatal(err)
	}
	got := FromUtxo(ex, SourceTrace, want[0].Prov.Repo, want[0].Prov.Commit, 1)
	if len(got) != 1 || !reflect.DeepEqual(got[0], want[0]) {
		t.Fatalf("harvested\n%+v\ncommitted\n%+v", got, want[0])
	}
	if p := got[0].Prov; p.Spec != "Utxo.tla" || p.Cfg != "UtxoGoTtlExclusiveBug.cfg" || p.Invariant != "GoAgrees" || p.Commit == "" {
		t.Errorf("provenance = %+v", p)
	}
}
