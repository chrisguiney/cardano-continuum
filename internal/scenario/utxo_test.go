package scenario

import (
	"context"
	"math/rand/v2"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

func testSeed() rng.Seed { return rng.Seed{1, 2, 3} }

// baseTx is Utxo.tla's Base: spend "a", balanced, fee 1.
func baseTx(era string) tla.AbstractTx {
	tx := tla.AbstractTx{Ins: []string{"a"}, Fee: 1, Cert: "none", OLock: "key", Valid: true, Pass: true, TColl: -1}
	if era == "Shelley" {
		tx.Hi = 3
	}
	return tx
}

func key(coin, tok int) tla.Output { return tla.Output{Coin: coin, Tok: tok, Lock: "key"} }

func utxoScenario(era string, start tla.Ledger, steps ...tla.Verdict) Scenario {
	return Scenario{
		Version: FormatVersion, ID: "ledger-utxo/test", Family: FamilyUtxo, Source: SourceVector,
		Prov: Provenance{Repo: "repo", Commit: "c0ffee", Spec: "Utxo.tla", Cfg: "Test.cfg", Invariant: "GoAgrees"},
		Utxo: &UtxoCase{Era: era, Start: start, Steps: steps},
	}
}

func oneOutput(slot int) tla.Ledger {
	return tla.Ledger{UTxO: map[string]tla.Output{"a": key(3, 0)}, Slot: slot, Registered: true}
}

// concretized returns the decoded transaction of the scenario's one step.
func concretized(t *testing.T, a *UtxoAdapter, s Scenario, start uint64) (*conway.ConwayTransaction, Step) {
	t.Helper()
	env := FakeBackend{}.Env()
	env.Seed, env.Start = testSeed(), start
	plan, err := a.Concretize(s, env)
	if err != nil {
		t.Fatal(err)
	}
	st := plan.Steps[len(plan.Steps)-1]
	if st.Kind != Submit || st.Index != 0 {
		t.Fatalf("last step = %+v, skipped %v truncated %v", st, plan.Skipped, plan.Truncated)
	}
	tx, err := conway.NewConwayTransactionFromCbor(st.Tx)
	if err != nil {
		t.Fatalf("crafted transaction does not decode: %v", err)
	}
	return tx, st
}

// TestUpperBoundOnTheSlot: a model transaction whose upper bound is the
// current slot becomes one whose invalid_hereafter is exactly the slot it is
// validated at; Shelley's inclusive TTL becomes the slot after, which keeps
// the validated slot the last valid one.
func TestUpperBoundOnTheSlot(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		era := tla.UtxoEras[r.IntN(len(tla.UtxoEras))]
		slot, start := r.IntN(3), r.Uint64N(1_000_000)
		tx := baseTx(era)
		tx.Hi = slot
		if era == "Shelley" && slot == 0 {
			continue
		}
		if era != "Shelley" && slot == 0 {
			continue // 0 is "no bound" from Allegra on
		}
		got, st := concretized(t, NewUtxoAdapter(), utxoScenario(era, oneOutput(slot), tla.Verdict{Tx: tx}), start)
		want := st.Slot
		if era == "Shelley" {
			want++
		}
		if got.TTL() != want {
			t.Fatalf("%s slot %d start %d: invalid_hereafter %d, validated at %d", era, slot, start, got.TTL(), st.Slot)
		}
	}
}

// TestLowerBoundOnTheSlot: lo equal to the slot becomes invalid_before equal
// to the validated slot.
func TestLowerBoundOnTheSlot(t *testing.T) {
	for slot := 1; slot <= 2; slot++ {
		tx := baseTx("Babbage")
		tx.Lo = slot
		got, st := concretized(t, NewUtxoAdapter(), utxoScenario("Babbage", oneOutput(slot), tla.Verdict{Tx: tx}), 40)
		if got.ValidityIntervalStart() != st.Slot {
			t.Fatalf("slot %d: invalid_before %d, validated at %d", slot, got.ValidityIntervalStart(), st.Slot)
		}
	}
}

// TestDeltaIsOneLovelace: the model's one-coin imbalance is exactly one
// lovelace, and a balanced transaction balances exactly.
func TestDeltaIsOneLovelace(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		for _, fee := range []int{1, 2} {
			tx := baseTx("Babbage")
			tx.Delta, tx.Fee = delta, fee
			s := utxoScenario("Babbage", oneOutput(1), tla.Verdict{Tx: tx})
			got, _ := concretized(t, NewUtxoAdapter(), s, 0)
			env := FakeBackend{}.Env()
			env.Seed = testSeed()
			pre, err := NewUtxoAdapter().Preconditions(s, env)
			if err != nil {
				t.Fatal(err)
			}
			unit := int64(pre.Params.MinFeeB)
			in := 3 * unit
			out := got.Outputs()[0].Amount().Int64() + got.Fee().Int64()
			if out-in != int64(delta) {
				t.Errorf("delta %d fee %d: produced - consumed = %d lovelace", delta, fee, out-in)
			}
			if got.Fee().Int64() != int64(fee)*unit {
				t.Errorf("fee %d: %d lovelace with a minimum of %d", fee, got.Fee().Int64(), unit)
			}
		}
	}
}

func run(t *testing.T, a Adapter, s Scenario) Result {
	t.Helper()
	res, err := runWith(context.Background(), s, a, FakeBackend{}, testSeed())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// TestTtlBoundaryGetsTheSpecVerdict replays the witnesses of
// UtxoGoTtlExclusiveBug (Shelley: a TTL equal to the slot is accepted) and of
// UtxoGoIntervalInclusiveBug (Allegra: an upper bound equal to the slot is
// rejected). The fake chain gives each the model's verdict.
func TestTtlBoundaryGetsTheSpecVerdict(t *testing.T) {
	shelley := baseTx("Shelley")
	shelley.Hi = 1
	allegra := baseTx("Allegra")
	allegra.Hi = 1
	for name, s := range map[string]Scenario{
		"UtxoGoTtlExclusiveBug":      utxoScenario("Shelley", oneOutput(1), tla.Verdict{Tx: shelley, Accepts: true}),
		"UtxoGoIntervalInclusiveBug": utxoScenario("Allegra", oneOutput(1), tla.Verdict{Tx: allegra, Accepts: false}),
	} {
		res := run(t, NewUtxoAdapter(), s)
		if res.Status != Pass || res.Ran != 1 || len(res.Findings) != 0 {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

// TestWrongMappingIsADisagreement: an adapter that puts the upper bound one
// slot late makes the fake accept what the model rejects.
func TestWrongMappingIsADisagreement(t *testing.T) {
	tx := baseTx("Allegra")
	tx.Hi = 1
	s := utxoScenario("Allegra", oneOutput(1), tla.Verdict{Tx: tx, Accepts: false})
	wrong := &UtxoAdapter{m: utxoMapping{upper: func(_ string, off uint64, hi int) uint64 { return off + uint64(hi) + 1 }}}
	res := run(t, wrong, s)
	if res.Status != Disagreement || len(res.Findings) != 1 {
		t.Fatalf("result = %+v", res)
	}
	f := res.Findings[0]
	if f.Kind != SpecDisagreement || f.ID == "" {
		t.Fatalf("finding = %+v", f)
	}
	facts := map[string]string{}
	for _, fact := range f.Facts {
		facts[fact.Name] = fact.Value
	}
	for name, want := range map[string]string{"repo": "repo", "commit": "c0ffee", "spec": "Utxo.tla", "cfg": "Test.cfg", "invariant": "GoAgrees"} {
		if facts[name] != want {
			t.Errorf("fact %s = %q, want %q", name, facts[name], want)
		}
	}
	if again := run(t, wrong, s); again.Findings[0].ID != f.ID {
		t.Error("finding ID is not stable across runs")
	}
}
