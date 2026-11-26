package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func scenarioCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(context.Background(), append([]string{"scenario"}, args...), &out)
	return out.String(), err
}

// TestScenarioRunLibrary runs the committed library on the fake chain and
// checks the summary's totals: three known disagreements, the rest passing.
func TestScenarioRunLibrary(t *testing.T) {
	out, err := scenarioCmd(t, "run", "-v", "--lib", "../../scenarios", "../../scenarios")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pass\tledger-utxo/UtxoGoTtlExclusiveBug/0",
		"spec_disagreement\tledger-utxo/UtxoGoSpentOnlyCollUnresolvedBug/0",
		"family source", "ledger-witnesses trace", "codec table",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unconcretizable\t") {
		t.Errorf("a curated scenario is unconcretizable:\n%s", out)
	}
}

// TestScenarioRunDeterministic: the same seed gives the same report.
func TestScenarioRunDeterministic(t *testing.T) {
	a, err := scenarioCmd(t, "run", "--lib", "../../scenarios", "--seed", "9", "ledger-utxo", "codec")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := scenarioCmd(t, "run", "--lib", "../../scenarios", "--seed", "9", "ledger-utxo", "codec")
	if a != b || !strings.Contains(a, "spec_disagreement ") {
		t.Fatalf("reports differ or name no finding:\n%s\n%s", a, b)
	}
}

func TestScenarioRunByID(t *testing.T) {
	out, err := scenarioCmd(t, "run", "--lib", "../../scenarios", "ledger-utxo/UtxoGoIntervalInclusiveBug/0")
	if err != nil || !strings.Contains(out, "total") {
		t.Fatalf("out %q err %v", out, err)
	}
}

func TestScenarioErrors(t *testing.T) {
	if _, err := scenarioCmd(t, "run", "--target", "live", "../../scenarios"); !errors.Is(err, errNoLive) {
		t.Errorf("live target: %v", err)
	}
	if _, err := scenarioCmd(t, "run", "--lib", "../../scenarios", "ledger-utxo/none"); err == nil {
		t.Error("missing scenario ran")
	}
	if _, err := scenarioCmd(t, "run"); err == nil {
		t.Error("run with no scenario accepted")
	}
	if _, err := scenarioCmd(t); !errors.Is(err, errScenarioUsage) {
		t.Errorf("no subcommand: %v", err)
	}
	if _, err := scenarioCmd(t, "bogus"); !errors.Is(err, errScenarioUsage) {
		t.Errorf("unknown subcommand: %v", err)
	}
	if _, err := scenarioCmd(t, "vectors"); err == nil {
		t.Error("vectors with no file accepted")
	}
}
