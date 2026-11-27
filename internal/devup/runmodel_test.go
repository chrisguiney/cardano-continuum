package devup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeTLC stands in for models/bin/tlc. It refuses a run whose Java temp dir
// is missing or whose metadata directory is the spec directory (where a
// concurrent run's states would be deleted), then prints <config>.out and
// exits with <config>.exit, both read from the spec directory.
const fakeTLC = `#!/bin/sh
cfg=""
meta=""
while [ $# -gt 0 ]; do
  case $1 in
    -config) cfg=$2; shift ;;
    -metadir) meta=$2; shift ;;
  esac
  shift
done
tmp=$(printf '%s' "$JAVA_TOOL_OPTIONS" | sed -n 's/.*-Djava.io.tmpdir=\([^ ]*\).*/\1/p')
[ -n "$tmp" ] && [ -d "$tmp" ] || { echo "fake tlc: no java.io.tmpdir"; exit 1; }
case $meta in
  "") echo "fake tlc: no -metadir"; exit 1 ;;
  "$PWD"/*) echo "fake tlc: -metadir inside the spec directory"; exit 1 ;;
esac
name=${cfg%.cfg}
cat "$name.out"
exit "$(cat "$name.exit" 2>/dev/null || echo 0)"
`

// fakeJava stands in for models/bin/_java; the runner only calls its
// --check preflight.
const fakeJava = `#!/bin/sh
[ "$1" = --check ] || exit 1
echo "toolchain: fake"
`

// TLC output for the outcomes the runner must tell apart.
const (
	passOut     = "Model checking completed. No error has been found.\n120 states generated, 100 distinct states found, 0 states left on queue.\nThe depth of the complete state graph search is 7.\n"
	invOut      = "Error: Invariant %s is violated.\n12 states generated, 11 distinct states found, 3 states left on queue.\n"
	actionOut   = "Error: Action property %s is violated.\n5 states generated, 4 distinct states found, 0 states left on queue.\n"
	temporalOut = "Error: Temporal properties were violated.\n9 states generated, 8 distinct states found, 0 states left on queue.\n"
	deadlockOut = "Error: Deadlock reached.\n7 states generated, 6 distinct states found, 0 states left on queue.\n"
	parseOut    = "Parse Error\nSemantic errors:\n"
)

// tlcCase is one config in a fake spec directory and what TLC prints for it.
type tlcCase struct {
	// cfg is the configuration's text.
	cfg string
	// out is what the fake TLC prints for it.
	out string
	// exit is the fake TLC's exit status.
	exit int
}

// modelDir builds models/{scripts,bin,specs/m} in a temp dir with the real
// runner, the fake tools and one config per case, and returns the models
// directory.
func modelDir(t *testing.T, cases map[string]tlcCase) string {
	t.Helper()
	runner, err := os.ReadFile("../../models/scripts/run-model.sh")
	if err != nil {
		t.Fatal(err)
	}

	models := filepath.Join(t.TempDir(), "models")
	write(t, models, "scripts/run-model.sh", string(runner))
	write(t, models, "bin/tlc", fakeTLC)
	write(t, models, "bin/_java", fakeJava)
	write(t, models, "specs/m/M.tla", "---- MODULE M ----\n====\n")

	for _, p := range []string{"scripts/run-model.sh", "bin/tlc", "bin/_java"} {
		if err := os.Chmod(filepath.Join(models, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for name, c := range cases {
		write(t, models, "specs/m/"+name+".cfg", c.cfg)
		write(t, models, "specs/m/"+name+".out", c.out)
		write(t, models, "specs/m/"+name+".exit", strconv.Itoa(c.exit))
	}

	return models
}

// runModel runs the runner on specs/m with a private lock and returns its
// output and exit status.
func runModel(t *testing.T, models string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "scripts/run-model.sh", "specs/m")
	cmd.Dir = models
	cmd.Env = append(os.Environ(), append([]string{"TLC_LOCK=" + filepath.Join(t.TempDir(), "lock"), "TMPDIR=" + t.TempDir()}, env...)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}

	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("run-model.sh: %v\n%s", err, out)
	}

	return string(out), ee.ExitCode()
}

const (
	oneInv    = "SPECIFICATION Spec\nINVARIANTS\n    Safe\n"
	oneProp   = "SPECIFICATION Spec\nPROPERTIES\n    Live\n"
	twoProps  = "SPECIFICATION Spec\nINVARIANTS\n    TypeOK\n    Safe\n"
	twoLive   = "SPECIFICATION Spec\n\\* EXPECT: Live\nPROPERTIES\n    Live\n    Other\n"
	expectDL  = "SPECIFICATION Spec\n\\* EXPECT: Deadlock\nINVARIANTS\n    Safe\n"
	sameLine  = "SPECIFICATION Spec\nINVARIANT Safe\n"
	actionCfg = "SPECIFICATION Spec\nPROPERTY Mono\n"
)

// A Bug config passes only when TLC reports the property the config names;
// a deadlock counts only when the config expects one.
func TestRunModelNamedProperty(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		c    tlcCase
		ok   bool
		want string
	}{
		{"named invariant", tlcCase{oneInv, sprintf(invOut, "Safe"), 12}, true, "fails  MBug.cfg on Safe (as expected)"},
		{"same-line invariant", tlcCase{sameLine, sprintf(invOut, "Safe"), 12}, true, "on Safe (as expected)"},
		{"other invariant", tlcCase{oneInv, sprintf(invOut, "TypeOK"), 12}, false, "WRONG  MBug.cfg: failed on TypeOK, expected Safe"},
		{"action property", tlcCase{actionCfg, sprintf(actionOut, "Mono"), 13}, true, "on Mono (as expected)"},
		{"unnamed temporal, one property", tlcCase{oneProp, temporalOut, 13}, true, "on Live (as expected)"},
		{"unnamed temporal, two properties", tlcCase{twoLive, temporalOut, 13}, false, "TLC does not name the temporal property"},
		{"deadlock not expected", tlcCase{oneInv, deadlockOut, 11}, false, "failed on Deadlock, expected Safe"},
		{"deadlock expected", tlcCase{expectDL, deadlockOut, 11}, true, "on Deadlock (as expected)"},
		{"two properties, no EXPECT", tlcCase{twoProps, sprintf(invOut, "Safe"), 12}, false, "names 2 properties"},
		{"passes", tlcCase{oneInv, passOut, 0}, false, "expected failure on Safe"},
		{"parse error", tlcCase{oneInv, parseOut, 255}, false, "could not check it"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			models := modelDir(t, map[string]tlcCase{"MBug": tc.c})
			out, code := runModel(t, models, "TLC_RECORD=1")

			if !strings.Contains(out, tc.want) {
				t.Errorf("output lacks %q:\n%s", tc.want, out)
			}

			if (code == 0) != tc.ok {
				t.Errorf("exit %d, want ok=%v:\n%s", code, tc.ok, out)
			}
		})
	}
}

// A passing config passes on TLC's completion line, and a violation in it
// names the property.
func TestRunModelPassingConfig(t *testing.T) {
	t.Parallel()
	models := modelDir(t, map[string]tlcCase{"M": {twoProps, passOut, 0}})
	if out, code := runModel(t, models, "TLC_RECORD=1"); code != 0 || !strings.Contains(out, "pass   M.cfg") {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	write(t, models, "specs/m/M.out", sprintf(invOut, "Safe"))
	write(t, models, "specs/m/M.exit", "12")

	if out, code := runModel(t, models); code == 0 || !strings.Contains(out, "WRONG  M.cfg: failed on Safe, expected pass") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The runner records each config's verdict, property and state counts in
// verdicts.tsv, and a later run that differs from it is wrong.
func TestRunModelVerdicts(t *testing.T) {
	t.Parallel()
	models := modelDir(t, map[string]tlcCase{
		"M":    {twoProps, passOut, 0},
		"MBug": {oneInv, sprintf(invOut, "Safe"), 12},
	})

	out, code := runModel(t, models)
	if code == 0 || !strings.Contains(out, "NEW    M.cfg") {
		t.Fatalf("a config with no baseline row must fail; exit %d:\n%s", code, out)
	}

	if out, code := runModel(t, models, "TLC_RECORD=1"); code != 0 {
		t.Fatalf("record: exit %d:\n%s", code, out)
	}

	tsv, err := os.ReadFile(filepath.Join(models, "specs/m/verdicts.tsv"))
	if err != nil {
		t.Fatal(err)
	}

	for _, row := range []string{"M.cfg\tpass\t-\t100\t7\t", "MBug.cfg\tfails\tSafe\t-\t-\t"} {
		if !strings.Contains(string(tsv), row) {
			t.Errorf("verdicts.tsv lacks %q:\n%s", row, tsv)
		}
	}

	if out, code := runModel(t, models); code != 0 {
		t.Fatalf("rerun against the baseline: exit %d:\n%s", code, out)
	}

	write(t, models, "specs/m/M.out", strings.Replace(passOut, "100 distinct", "90 distinct", 1))

	out, code = runModel(t, models)
	if code == 0 || !strings.Contains(out, "CHANGED M.cfg: distinct 100 -> 90") {
		t.Fatalf("a changed distinct count must fail; exit %d:\n%s", code, out)
	}

	if err := os.Remove(filepath.Join(models, "specs/m/MBug.cfg")); err != nil {
		t.Fatal(err)
	}

	out, _ = runModel(t, models)
	if !strings.Contains(out, "GONE   MBug.cfg") {
		t.Fatalf("a baseline row with no config must be reported:\n%s", out)
	}
}

// A missing toolchain is one error, not a WRONG line per config, and the
// runner never deletes a states directory it did not create.
func TestRunModelPreflightAndIsolation(t *testing.T) {
	t.Parallel()
	models := modelDir(t, map[string]tlcCase{"M": {twoProps, passOut, 0}})
	write(t, models, "specs/m/states/other-run.st", "x")
	write(t, models, "bin/_java", "#!/bin/sh\necho 'TLA+ is not installed yet. Run: make models-install' >&2\nexit 2\n")

	out, code := runModel(t, models)
	if code != 2 || strings.Contains(out, "WRONG") || !strings.Contains(out, "make models-install") {
		t.Fatalf("exit %d, want 2 and one install message:\n%s", code, out)
	}

	write(t, models, "bin/_java", fakeJava)

	if out, code := runModel(t, models, "TLC_RECORD=1"); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}

	if _, err := os.Stat(filepath.Join(models, "specs/m/states/other-run.st")); err != nil {
		t.Fatalf("the runner removed another run's states: %v", err)
	}
}

// Only one TLC run proceeds at a time: a live holder of the lock blocks the
// runner, and a dead holder's lock is taken over.
func TestRunModelLock(t *testing.T) {
	t.Parallel()
	models := modelDir(t, map[string]tlcCase{"M": {twoProps, passOut, 0}})
	lock := filepath.Join(t.TempDir(), "lock")
	write(t, lock, "pid", strconv.Itoa(os.Getpid()))

	out, code := runModel(t, models, "TLC_LOCK="+lock, "TLC_LOCK_WAIT=1")
	if code != 2 || !strings.Contains(out, "held by pid") {
		t.Fatalf("exit %d, want 2 while the lock is held:\n%s", code, out)
	}

	write(t, lock, "pid", "999999")

	if out, code := runModel(t, models, "TLC_LOCK="+lock, "TLC_LOCK_WAIT=1", "TLC_RECORD=1"); code != 0 {
		t.Fatalf("a dead holder's lock must be taken over; exit %d:\n%s", code, out)
	}

	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("the runner left its lock behind: %v", err)
	}
}

func sprintf(format, name string) string {
	return strings.Replace(format, "%s", name, 1)
}
