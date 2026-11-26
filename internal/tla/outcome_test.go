package tla

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		exit    int
		timeout bool
		want    Outcome
		inv     string
	}{
		{"invariant", "TLC2 Version 2026.11.19.180144 (rev: bc1b506)\nError: Invariant NeverDoneAfterPong is violated.\nError: The behavior up to this point is:\n", 12, false, Violation, "NeverDoneAfterPong"},
		{"deadlock", "Error: Deadlock reached.\n", 11, false, Violation, "Deadlock"},
		{"property", "Error: Temporal properties were violated.\n", 13, false, Violation, "Temporal"},
		{"action property", "Error: Action property Mono is violated.\n", 13, false, Violation, "Mono"},
		{"pass", "Model checking completed. No error has been found.\n", 0, false, Passed, ""},
		{"eval error", "Error: Evaluating invariant Nope failed.\nError: The invariant Nope is not defined\n", 255, false, Broken, ""},
		{"parse error", "Parse Error\nSemantic errors:\n", 255, false, Broken, ""},
		{"timeout", "Progress(3)\n", -1, true, TimedOut, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, inv := Classify(tt.out, tt.exit, tt.timeout)
			if got != tt.want || inv != tt.inv {
				t.Fatalf("got %v %q, want %v %q", got, inv, tt.want, tt.inv)
			}
		})
	}
}

func TestTLCVersion(t *testing.T) {
	out := "Picked up JAVA_TOOL_OPTIONS: x\nTLC2 Version 2026.11.19.180144 (rev: bc1b506)\n"
	if got, want := TLCVersion(out), "2026.11.19.180144 (rev: bc1b506)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := TLCVersion("nothing"); got != "" {
		t.Fatalf("got %q", got)
	}
}
