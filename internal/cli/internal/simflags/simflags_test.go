package simflags

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/profiles"
)

func TestResolveProfilesNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"m1", "preview", "preprod", "mainnet"} {
		raw, label, err := ResolveProfiles(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := profile.ParseNamed(label, bytes.NewReader(raw)); err != nil {
			t.Fatalf("%s does not parse: %v", name, err)
		}
	}
	if _, _, err := ResolveProfiles("nosuch"); err == nil || !strings.Contains(err.Error(), "preprod") {
		t.Fatalf("an unknown name should list the shipped names: %v", err)
	}
}

func TestResolveProfilesPath(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "mine.yaml")
	if err := os.WriteFile(p, profiles.M1, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, label, err := ResolveProfiles(p)
	if err != nil || label != p || !bytes.Equal(raw, profiles.M1) {
		t.Fatalf("path: label %q err %v", label, err)
	}
	if _, _, err := ResolveProfiles(filepath.Join(t.TempDir(), "gone.yaml")); err == nil {
		t.Fatal("a missing file resolved")
	}
}

// --scale multiplies base rates and changes the run's fingerprint, but not
// the profile hash, which names the fitted file and selects its target.
func TestScaleFlag(t *testing.T) {
	t.Parallel()
	cfg := func(args ...string) (hash, genesis []byte, rate float64) {
		t.Helper()
		var sf Flags
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		sf.Register(fs)
		if err := fs.Parse(append([]string{"--profiles", "preprod", "--wallets", "10"}, args...)); err != nil {
			t.Fatal(err)
		}
		c, err := sf.Config()
		if err != nil {
			t.Fatal(err)
		}
		h, err := c.Header()
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Profiles.Get(c.Profiles.Names()[0])
		return h.ProfileHash, h.GenesisHash, p.Rates.Base[profile.IntentSend]
	}
	h1, g1, r1 := cfg()
	h2, g2, r2 := cfg("--scale", "4")
	if r2 != 4*r1 {
		t.Errorf("rate %v, want 4 x %v", r2, r1)
	}
	if !bytes.Equal(h1, h2) {
		t.Error("scale changed the profile hash")
	}
	if bytes.Equal(g1, g2) {
		t.Error("scale is not fingerprinted: replay under another scale would be accepted")
	}
	var sf Flags
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	sf.Register(fs)
	if err := fs.Parse([]string{"--scale", "0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sf.Config(); err == nil {
		t.Fatal("scale 0 accepted")
	}
}

// Wallets split by profile weight, largest remainder first.
func TestWeightedCohorts(t *testing.T) {
	t.Parallel()
	doc := func(w ...string) *profile.Registry {
		y := "version: 1\nprofiles:\n"
		for i, x := range w {
			y += "  - name: p" + string(rune('a'+i)) + "\n    kind: wallet\n    agenda: hodl\n" + x +
				"    traits: {competence: \"beta(2, 2)\", patience: \"const(1)\", learning_rate: \"const(0)\", care: \"beta(2, 2)\"}\n"
		}
		ps, err := profile.Parse(strings.NewReader(y))
		if err != nil {
			t.Fatal(err)
		}
		reg, err := profile.NewRegistry(ps)
		if err != nil {
			t.Fatal(err)
		}
		return reg
	}
	counts := func(reg *profile.Registry, n int) []int {
		cs, err := CohortsFor(reg, n)
		if err != nil {
			t.Fatal(err)
		}
		var out []int
		for _, c := range cs {
			out = append(out, c.Count)
		}
		return out
	}
	if got := counts(doc("    weight: 0.7\n", "    weight: 0.3\n"), 10); got[0] != 7 || got[1] != 3 {
		t.Errorf("weighted: %v", got)
	}
	if got := counts(doc("    weight: 0.55\n", "    weight: 0.25\n", "    weight: 0.2\n"), 7); got[0]+got[1]+got[2] != 7 || got[0] != 4 {
		t.Errorf("remainders: %v", got)
	}
	if got := counts(doc("", ""), 5); got[0] != 3 || got[1] != 2 {
		t.Errorf("unweighted split: %v", got)
	}
	if _, err := CohortsFor(doc("    weight: 0.5\n", ""), 4); err == nil {
		t.Error("mixed weighted and unweighted accepted")
	}
}

// --hard-fork unset is the default schedule, a fork to 11.0 at epoch 3,
// where the genesis version can follow it; "none" turns it off, and the
// resolved schedule is in the run's identity and its replay flags.
func TestHardForkFlag(t *testing.T) {
	t.Parallel()
	cfg := func(args ...string) (Flags, engineHeader) {
		t.Helper()
		var sf Flags
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		sf.Register(fs)
		if err := fs.Parse(append([]string{"--profiles", "preprod", "--wallets", "10"}, args...)); err != nil {
			t.Fatal(err)
		}
		c, err := sf.Config()
		if err != nil {
			t.Fatal(err)
		}
		h, err := c.Header()
		if err != nil {
			t.Fatal(err)
		}
		return sf, engineHeader{sched: c.HardFork.String(), genesis: h.GenesisHash}
	}
	defSF, def := cfg()
	_, none := cfg("--hard-fork", "none")
	_, late := cfg("--hard-fork", "11.0@epoch:5")
	if def.sched != "11.0@epoch:3" || none.sched != "none" || late.sched != "11.0@epoch:5" {
		t.Fatalf("schedules %q, %q, %q", def.sched, none.sched, late.sched)
	}

	if bytes.Equal(def.genesis, none.genesis) || bytes.Equal(def.genesis, late.genesis) {
		t.Fatal("the schedule is not in the genesis hash")
	}

	if got := strings.Join(defSF.Oracle(false, protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 3}).Flags, " "); !strings.Contains(got, "--hard-fork=11.0@epoch:3") {
		t.Fatalf("replay flags %q omit the schedule", got)
	}

	if s, err := defSF.HardForkSchedule(protocol.Version{Major: 11}); err != nil || s.On() {
		t.Fatalf("default from 11.0: %v, %v; want none, the fork is already in force", s, err)
	}

	var bad Flags
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	bad.Register(fs)
	if err := fs.Parse([]string{"--hard-fork", "banana"}); err != nil {
		t.Fatal(err)
	}

	if _, err := bad.HardForkSchedule(protocol.Version{Major: 10}); err == nil {
		t.Fatal("banana parsed")
	}
}

type engineHeader struct {
	sched   string
	genesis []byte
}
