package sim

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

var marketRE = regexp.MustCompile(`markets: buys (.+); sells (.+); births (.+); largest drawdown (-?[0-9.]+)% \(\d+s-\d+s\): hodlers (\d+), sellers (\d+)`)

// A default (m1) fake run trades on every side of the market and reports
// it: buys and sells per exchange, births per currency, and the split of
// wallets that held or sold in the largest drawdown.
func TestFakeRunMarkets(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	args := []string{"--fake", "--epochs", "2", "--seed", "7", "--out", filepath.Join(t.TempDir(), "run")}
	if err := Run(context.Background(), args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	t.Log(out.String())
	m := marketRE.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no markets line in the summary:\n%s", out.String())
	}
	if m[1] == "none" || m[2] == "none" {
		t.Errorf("buys %q sells %q: want both", m[1], m[2])
	}
	hodl, _ := strconv.Atoi(m[5])
	sell, _ := strconv.Atoi(m[6])
	if hodl+sell == 0 {
		t.Errorf("no wallet decided under water in the largest drawdown")
	}
}
