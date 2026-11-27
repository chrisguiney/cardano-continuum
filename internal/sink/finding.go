package sink

import (
	"fmt"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

func optStr(t *table, c int, v string) {
	if v == "" {
		t.null(c)
		return
	}
	t.str(c, v)
}

func verdictsText(vs []oracle.Verdict) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		cs := make([]string, len(v.Classes))
		for j, c := range v.Classes {
			cs[j] = string(c)
		}
		parts[i] = strings.TrimSpace(fmt.Sprintf("%s@%s=%s %s", v.Node.Impl, v.Node.Pool, v.Verdict, strings.Join(cs, ",")))
	}
	return strings.Join(parts, ";")
}

func factsText(fs []oracle.Fact) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Name + "=" + f.Value
	}
	return strings.Join(parts, ";")
}
