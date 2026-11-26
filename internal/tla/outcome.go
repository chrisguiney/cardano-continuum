package tla

import (
	"regexp"
	"strings"
)

// Outcome is what a TLC run on a configuration amounted to.
type Outcome string

const (
	// Violation means TLC found a counterexample: an invariant, property or
	// deadlock failure.
	Violation Outcome = "violation"
	// Passed means TLC completed with no error.
	Passed Outcome = "passed"
	// Broken means TLC could not check the configuration: a parse or
	// evaluation error. It is a model defect, not a finding.
	Broken Outcome = "broken"
	// TimedOut means the run hit its bound before finishing.
	TimedOut Outcome = "timeout"
)

var (
	violated = regexp.MustCompile(`(?m)^Error: (?:Invariant|Action property|Property|Temporal property) (\S+)(?: was| is) violated`)
	temporal = regexp.MustCompile(`(?m)^Error: Temporal properties were violated`)
	deadlock = regexp.MustCompile(`(?m)^Error: Deadlock reached`)
	passed   = regexp.MustCompile(`(?m)^Model checking completed\. No error has been found`)
	version  = regexp.MustCompile(`(?m)^TLC2 Version (.+)$`)
)

// Classify decides the outcome of a TLC run from its combined output, exit
// status and whether the bound expired. For a violation it also returns the
// violated invariant or property name ("Deadlock" and "Temporal" stand for the
// two kinds that name none). Only a reported violation counts as a finding; a
// non-zero exit without one is Broken.
func Classify(out string, exit int, timedOut bool) (Outcome, string) {
	if timedOut {
		return TimedOut, ""
	}
	if m := violated.FindStringSubmatch(out); m != nil {
		return Violation, m[1]
	}
	if deadlock.MatchString(out) {
		return Violation, "Deadlock"
	}
	if temporal.MatchString(out) {
		return Violation, "Temporal"
	}
	if exit == 0 && passed.MatchString(out) {
		return Passed, ""
	}
	return Broken, ""
}

// TLCVersion returns the version TLC prints on its banner line, or "".
func TLCVersion(out string) string {
	m := version.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}
