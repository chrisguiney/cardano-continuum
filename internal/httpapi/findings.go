package httpapi

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Findings is what serves the findings page: the engine's oracle.Set on a
// live run, or the findings a log holds.
type Findings interface {
	// Page returns the findings from offset, at most limit of them, in
	// order of first occurrence, and how many there are in all.
	Page(offset, limit int) ([]oracle.Finding, int)
}

// FindingsPage is one page of fs in the API's shape. A page past the end is
// empty, not an error.
func FindingsPage(fs Findings, p Page) FindingPage {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	page, total := fs.Page(p.Offset, p.Limit)
	rows := make([]Finding, 0, len(page))
	for _, f := range page {
		rows = append(rows, FindingOf(f))
	}
	return FindingPage{Total: total, Offset: p.Offset, Limit: p.Limit, Rows: rows}
}

// FindingOf converts a finding to the API's shape. The expected verdict set
// is the first verdict, from source "expected"; a monitor finding lists the
// nodes it involves with verdict "involved".
func FindingOf(f oracle.Finding) Finding {
	out := Finding{
		ID: f.ID, Seq: f.Seq,
		Summary:      fmt.Sprintf("%s %s: %s", f.Severity, f.Kind, f.Summary),
		TxCBORHex:    hex.EncodeToString(f.Tx),
		Verdicts:     []ImplVerdict{},
		Reproducible: f.Reproducible, ReplayCommand: f.Replay,
	}
	if f.Expected != nil {
		out.Verdicts = append(out.Verdicts, ImplVerdict{Impl: "expected", Verdict: f.Expected.String()})
	}
	for _, v := range f.Verdicts {
		text := v.Verdict
		if len(v.Classes) > 0 {
			text += " " + classList(v.Classes)
		}
		out.Verdicts = append(out.Verdicts, implVerdict(v.Node, text))
	}
	if len(f.Verdicts) == 0 {
		for _, n := range f.Nodes {
			out.Verdicts = append(out.Verdicts, implVerdict(n, "involved"))
		}
	}
	return out
}

func implVerdict(n oracle.Node, verdict string) ImplVerdict {
	return ImplVerdict{Impl: n.Impl, Verdict: verdict, Build: buildText(n), Dirty: n.Dirty}
}

// buildText is the pool, the image digest and each dependency's revision,
// sorted by dependency.
func buildText(n oracle.Node) string {
	var parts []string
	if n.Pool != "" {
		parts = append(parts, "pool="+n.Pool)
	}
	if n.Digest != "" {
		parts = append(parts, n.Digest)
	}
	for _, dep := range sortedKeys(n.Revisions) {
		parts = append(parts, dep+"="+n.Revisions[dep])
	}
	return strings.Join(parts, " ")
}

func classList(cs []submit.Class) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ",")
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}
