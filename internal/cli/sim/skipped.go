package sim

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// numRE matches the amounts and ids that make otherwise equal skip reasons
// differ.
var numRE = regexp.MustCompile(`[0-9]+`)

// skipReason folds a skip's why into a reason shared by every instance.
func skipReason(why string) string { return numRE.ReplaceAllString(why, "N") }

// topSkips lists the n largest counts as "reason=count", ties by reason.
func topSkips(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if m[a] != m[b] {
			return m[b] - m[a]
		}
		return strings.Compare(a, b)
	})
	keys = keys[:min(n, len(keys))]
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return out
}

// skippedLine reports the most common skip reasons by intent kind, so a
// run that skips far more than it submits says why.
func skippedLine(ctx context.Context, r eventlog.Reader) (string, error) {
	m := map[string]int{}
	err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Kind != engine.KindSkipped {
			return nil
		}
		var s engine.Skipped
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			return err
		}
		m[s.Kind+": "+skipReason(s.Why)]++
		return nil
	})
	if err != nil || len(m) == 0 {
		return "", err
	}
	return "skipped by reason: " + strings.Join(topSkips(m, 8), "; ") + "\n", nil
}
