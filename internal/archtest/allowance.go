package archtest

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// parseAllowance reads allowance.txt: one "rule package [name] size" entry
// per line, "#" starting a comment line. The map key is the entry without
// its size. The size is the largest measurement the entry excuses.
func parseAllowance(src string) (map[string]int, error) {
	out := map[string]int{}
	for i, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cut := strings.LastIndexByte(line, ' ')
		if cut < 0 {
			return nil, fmt.Errorf("allowance line %d: want \"<rule> <package> [<name>] <size>\", got %q", i+1, line)
		}
		n, err := strconv.Atoi(line[cut+1:])
		if err != nil {
			return nil, fmt.Errorf("allowance line %d: size %q: %w", i+1, line[cut+1:], err)
		}
		out[line[:cut]] = n
	}
	return out, nil
}

// compare returns the findings the allowance does not cover (absent, or
// larger than the entry's size) and the allowance entries no finding needs.
// Both are formatted as allowance lines.
func compare(found []finding, allowed map[string]int) (fresh, stale []string) {
	seen := map[string]bool{}
	for _, f := range found {
		seen[f.key] = true
		if max, ok := allowed[f.key]; !ok || f.n > max {
			fresh = append(fresh, fmt.Sprintf("%s %d", f.key, f.n))
		}
	}
	for key, n := range allowed {
		if !seen[key] {
			stale = append(stale, fmt.Sprintf("%s %d", key, n))
		}
	}
	sort.Strings(stale)
	return fresh, stale
}

// tighten rewrites the allowance text so each entry holds the current
// measurement and entries with no finding are dropped. It never raises a
// size or adds an entry. Comments and blank lines stay.
func tighten(src string, found []finding) string {
	now := map[string]int{}
	for _, f := range found {
		now[f.key] = f.n
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(src, "\n"), "\n") {
		t := strings.TrimSpace(line)
		cut := strings.LastIndexByte(t, ' ')
		if t == "" || strings.HasPrefix(t, "#") || cut < 0 {
			out = append(out, line)
			continue
		}
		key := t[:cut]
		n, ok := now[key]
		if !ok {
			continue
		}
		old, err := strconv.Atoi(t[cut+1:])
		if err == nil && n < old {
			line = fmt.Sprintf("%s %d", key, n)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}
