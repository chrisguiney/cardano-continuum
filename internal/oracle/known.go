package oracle

import (
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// rejectOnly starts Expectation.String for a rejection that must name only
// the listed classes.
const rejectOnly = "reject naming only "

// KnownIssue is an upstream defect whose findings are already understood.
// A finding that matches is still recorded and served; it is only tagged so
// a reader can set it aside and look at what is new. A matcher is exact on
// each field it names, so a finding of any other shape stays untagged.
type KnownIssue struct {
	// Ref names the upstream issue, as "repo#number".
	Ref string
	URL string
	// Summary says what the defect is and what it makes the oracle report.
	Summary string
	// Kind is the finding kind the defect produces.
	Kind Kind
	// Expects is a class the finding's "expected" fact must list among the
	// classes a rejection could name; the fact is Expectation.String, which
	// starts "reject naming only " for a rejection that must name some of
	// them.
	Expects submit.Class
	// Verdict is the finding's "verdict" fact: what the node answered.
	Verdict string
}

// KnownIssues is the registry, in match order.
var KnownIssues = []KnownIssue{
	{
		Ref:     "gouroboros#3347",
		URL:     "https://github.com/blinklabs-io/gouroboros/issues/3347",
		Summary: "A Conway MissingVKeyWitnessesUTXOW rejection decodes as a generic error, so the class is reported as Unknown.",
		Kind:    Unclassified,
		Expects: submit.MissingVKeyWitnesses,
		Verdict: "rejected Unknown",
	},
}

// KnownIssueOf is the registry entry f matches, if any.
func KnownIssueOf(f Finding) (KnownIssue, bool) {
	for _, k := range KnownIssues {
		if k.matches(f) {
			return k, true
		}
	}
	return KnownIssue{}, false
}

func (k KnownIssue) matches(f Finding) bool {
	if f.Kind != k.Kind || factValue(f, "verdict") != k.Verdict {
		return false
	}
	list, ok := strings.CutPrefix(factValue(f, "expected"), rejectOnly)

	return ok && slices.Contains(strings.Split(list, ","), string(k.Expects))
}

// factValue is the value of f's first fact named name, or "".
func factValue(f Finding, name string) string {
	for _, x := range f.Facts {
		if x.Name == name {
			return x.Value
		}
	}
	return ""
}
