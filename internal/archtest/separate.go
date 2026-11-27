package archtest

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// apart names, for each binary, the packages it must not link through any
// chain of imports. The engine must not link the web UI: up.sh resets the
// devnet whenever the engine image changes, since a run is resumed only by
// the binary that wrote its log, so a UI change baked into the engine would
// reset every run. continuum-ui serves the UI instead.
var apart = map[string][]string{
	"cmd/continuum": {"web"},
}

// separations returns one message per package in apart that its binary
// reaches, with the import chain that reaches it, sorted by binary.
func separations(g graph) []string {
	bins := make([]string, 0, len(apart))
	for b := range apart {
		bins = append(bins, b)
	}
	sort.Strings(bins)

	var out []string
	for _, bin := range bins {
		for _, pkg := range apart[bin] {
			if c := chain(g, bin, pkg); c != nil {
				out = append(out, fmt.Sprintf("%s reaches %s: %s", bin, pkg, strings.Join(c, " -> ")))
			}
		}
	}
	return out
}

// chain returns a shortest import chain from one package to another in g,
// both ends included, or nil when from does not reach to.
func chain(g graph, from, to string) []string {
	prev := map[string]string{from: ""}
	queue := []string{from}

	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if pkg == to {
			return walkBack(prev, to)
		}

		for _, imp := range g[pkg] {
			if _, seen := prev[imp]; !seen {
				prev[imp] = pkg
				queue = append(queue, imp)
			}
		}
	}
	return nil
}

// walkBack follows prev from to back to the search's start.
func walkBack(prev map[string]string, to string) []string {
	var c []string
	for p := to; p != ""; p = prev[p] {
		c = append(c, p)
	}
	slices.Reverse(c)
	return c
}
