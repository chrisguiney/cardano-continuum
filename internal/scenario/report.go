package scenario

import (
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
)

func sortByID(ss []Scenario) {
	sort.Slice(ss, func(i, j int) bool { return ss[i].ID < ss[j].ID })
}

// Tally counts scenarios by how they ended.
type Tally struct {
	Run, Pass, Disagreement, Unconcretizable int
	// StepsSkipped counts abstract steps left out of scenarios that ran.
	StepsSkipped int
}

// Summary is the outcome of a batch of scenarios.
type Summary struct {
	// Groups are the tallies per family and source, keyed "family source".
	Groups map[string]Tally
	// Reasons counts unconcretizable scenarios and skipped steps by reason.
	Reasons map[string]int
	Results []Result
}

// Summarize tallies results.
func Summarize(results []Result) Summary {
	sum := Summary{Groups: map[string]Tally{}, Reasons: map[string]int{}, Results: results}
	for _, r := range results {
		key := r.Family + " " + string(r.Source)
		t := sum.Groups[key]
		t.Run++
		switch r.Status {
		case Pass:
			t.Pass++
		case Disagreement:
			t.Disagreement++
		case Unconcretizable:
			t.Unconcretizable++
			sum.Reasons[r.Reason]++
		}
		if r.Status != Unconcretizable {
			for _, sk := range r.Skipped {
				t.StepsSkipped++
				sum.Reasons[sk.Reason]++
			}
			if r.Truncated != nil {
				t.StepsSkipped += r.Steps - r.Truncated.Index
				sum.Reasons[r.Truncated.Reason]++
			}
		}
		sum.Groups[key] = t
	}
	return sum
}

// Total adds every group.
func (s Summary) Total() Tally {
	var t Tally
	for _, g := range s.Groups {
		t.Run += g.Run
		t.Pass += g.Pass
		t.Disagreement += g.Disagreement
		t.Unconcretizable += g.Unconcretizable
		t.StepsSkipped += g.StepsSkipped
	}
	return t
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Write prints the summary table, the reasons scenarios or steps could not be
// concretized, and each disagreement.
func (s Summary) Write(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	row := func(name string, t Tally) {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%d\n", name, t.Run, t.Pass, t.Disagreement, t.Unconcretizable, t.StepsSkipped)
	}
	_, _ = fmt.Fprintln(tw, "family source\trun\tpass\tspec_disagreement\tunconcretizable\tsteps skipped")
	for _, k := range sortedKeys(s.Groups) {
		row(k, s.Groups[k])
	}
	row("total", s.Total())
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(s.Reasons) > 0 {
		_, _ = fmt.Fprintln(w, "\nnot concretized (scenarios and steps), by reason:")
	}
	for _, k := range sortedKeys(s.Reasons) {
		_, _ = fmt.Fprintf(w, "  %4d  %s\n", s.Reasons[k], k)
	}
	for _, r := range s.Results {
		for _, f := range r.Findings {
			_, _ = fmt.Fprintf(w, "\n%s %s\n  %s\n", f.Kind, f.ID, f.Summary)
		}
	}
	return nil
}
