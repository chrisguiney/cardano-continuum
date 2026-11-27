package sim

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/profiles"
)

// govProfile is a shipped fitted profile with its governance rates (SPO and
// DRep votes, proposals, the civic wallets' activity) raised by boost, every DRep a voter and each pool
// voting at least daily, so a run of days shows what a fitted one shows
// over months: preprod's pools cast six votes in the fitted 90 days. The
// other wallets' rates are the fitted ones.
func govProfile(t *testing.T, name string, boost float64) string {
	t.Helper()
	raw, err := profiles.Named(name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := profile.ParseFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range f.Profiles {
		if p.Agenda == "civic" {
			for k := range p.Rates.Base {
				p.Rates.Base[k] *= boost
			}
		}
	}
	for _, p := range f.Populations {
		if p.Pool != nil {
			p.Pool.Vote = max(p.Pool.Vote*boost, 1)
		}
		if p.DRep != nil {
			p.DRep.Vote, p.DRep.Voting = p.DRep.Vote*boost, 1
		}
	}
	for k := range f.Network.Proposals {
		f.Network.Proposals[k] *= boost
	}
	out, err := profile.MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var govLine = regexp.MustCompile(`governance: proposals (\d+) \(.*\); votes committee=(\d+) drep=(\d+) spo=(\d+); vote delegations (\d+)`)

// A fake run of a fitted profile with 100 wallets over three epochs makes
// proposals, DRep and SPO votes and the citizens' vote delegations, and
// the summary reports each count; no oracle finding. Preprod's voter
// cluster is 0.2% of its wallets, one of 100, whose drawn activity is too
// low to delegate in three days; mainnet's 6% are six.
func TestFakeRunGovernance(t *testing.T) {
	for name, civic := range map[string]bool{"preprod": false, "mainnet": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			args := []string{"--fake", "--epochs", "3", "--epoch-slots", "86400", "--block-every", "50", "--seed", "7",
				"--wallets", "100", "--profiles", govProfile(t, name, 100), "--out", filepath.Join(t.TempDir(), "run")}
			if err := Run(context.Background(), args, &out); err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			t.Log(out.String())
			m := govLine.FindStringSubmatch(out.String())
			if m == nil {
				t.Fatalf("no governance line in the summary:\n%s", out.String())
			}
			n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
			if n(1) == 0 || n(3) == 0 || n(4) == 0 || civic && n(5) == 0 {
				t.Errorf("proposals %d, DRep votes %d, SPO votes %d, vote delegations %d: want each", n(1), n(3), n(4), n(5))
			}
			if !regexp.MustCompile(`findings 0 \(fatal 0\)`).MatchString(out.String()) {
				t.Errorf("findings in a clean run:\n%s", out.String())
			}
		})
	}
}
