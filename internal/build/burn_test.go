package build

import (
	"testing"
)

// TestBurnSpendsMemory: the burn loop's memory grows with n squared and
// tracks steps/160, so declaring its measured units declares work done.
func TestBurnSpendsMemory(t *testing.T) {
	h := newHarness(t)
	var prev int64
	for _, n := range []int{50, 100} {
		req := Mint{Spend: h.spend(), Policy: Burn(n), Name: []byte("burn"), Quantity: 1, Redeemer: unitDatum(), Collateral: 12_000_000}
		got, err := h.b.Plutus().Mint(ctx, h.alice, req)
		if err != nil {
			t.Fatal(err)
		}
		h.checkExUnits(got, req.Inputs)
		tx := h.check(got, req.Inputs, want{}, func() (Built, error) { return h.b.Plutus().Mint(ctx, h.alice, req) })
		if c := tx.TotalCollateral(); c == nil || c.Uint64() != 12_000_000 {
			t.Fatalf("total collateral %v, want the requested 12 ADA", c)
		}
		used, err := h.chain.Evaluate(got.Tx)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range used {
			if r := float64(u.Steps) / float64(u.Memory); r < 159 || r > 161 {
				t.Errorf("n=%d: steps/memory %.1f, want about 160", n, r)
			}
			if u.Memory < 3*prev {
				t.Errorf("n=%d: memory %d does not grow with n squared from %d", n, u.Memory, prev)
			}
			prev = u.Memory
		}
	}
}

func TestBurnSize(t *testing.T) {
	h := newHarness(t)
	const want = 5_000_000
	req := Mint{Spend: h.spend(), Policy: Burn(BurnSize(want)), Name: []byte("burn"), Quantity: 1, Redeemer: unitDatum(), Collateral: 12_000_000}
	got, err := h.b.Plutus().Mint(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	u, err := Declared(got.Tx)
	if err != nil {
		t.Fatal(err)
	}
	if d := float64(u.Memory-want) / want; d < -0.05 || d > 0.05 {
		t.Fatalf("BurnSize(%d) spends %d memory, %.1f%% off", want, u.Memory, 100*d)
	}
}
