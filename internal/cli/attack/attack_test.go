package attack_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeterminismCLIAttack runs a control and an attack fake run on one
// seed, replays the attack run, and compares the two logs.
func TestDeterminismCLIAttack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	flags := []string{"--seed", "7", "--wallets", "12", "--epoch-slots", "120", "--profiles", "m3"}
	attack := append([]string{"--attack", "dust-flood,malformed-cbor"}, flags...)
	for name, fl := range map[string][]string{"control": flags, "attack": attack} {
		var out bytes.Buffer
		args := append([]string{"run", "--fake", "--epochs", "2", "--out", filepath.Join(dir, name)}, fl...)
		if err := run(ctx, args, &out); err != nil {
			t.Fatalf("%s run: %v\n%s", name, err, out.String())
		}
		if name == "attack" && !strings.Contains(out.String(), "attacks: steps ") {
			t.Fatalf("attack run summary has no attacks: %s", out.String())
		}
	}
	logPath := filepath.Join(dir, "attack", "events.log")
	var out bytes.Buffer
	if err := run(ctx, append(append([]string{"replay"}, attack...), logPath), &out); err != nil {
		t.Fatalf("replay: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), ": 0 mismatches") {
		t.Fatalf("replay output: %s", out.String())
	}
	out.Reset()
	if err := run(ctx, []string{"compare", filepath.Join(dir, "control", "events.log"), logPath}, &out); err != nil {
		t.Fatalf("compare: %v", err)
	}
	for _, want := range []string{"utxo_growth *", "fatal_findings *", "attack_steps"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("compare table lacks %q:\n%s", want, out.String())
		}
	}
}

func TestAttackFlagRefusesUnknown(t *testing.T) {
	err := run(context.Background(), []string{"run", "--fake", "--attack", "teleport", "--out", t.TempDir()}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "teleport") {
		t.Fatalf("unknown attack: %v", err)
	}
}

// TestGapAttackRun: --attack exunits-block-gap runs the fake chain on the
// attack's inconsistent genesis, so the adversary attacks; the summary
// names the step and the run files no fatal finding.
func TestGapAttackRun(t *testing.T) {
	var out bytes.Buffer
	args := []string{"run", "--fake", "--epochs", "1", "--out", t.TempDir(), "--seed", "7", "--wallets", "12",
		"--epoch-slots", "120", "--profiles", "m3", "--attack", "exunits-block-gap"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "attacks: steps ") {
		t.Fatalf("the gap adversary made no step: %s", out.String())
	}
	if !strings.Contains(out.String(), "(fatal 0)") {
		t.Fatalf("fatal findings: %s", out.String())
	}
}
