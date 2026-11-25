package plan

import (
	"cmp"
	"slices"
)

// Select returns outputs from avail worth at least need, largest first with
// Ref breaking ties, or nil when avail cannot cover need. It keeps the input
// count low and depends only on its arguments, never on their order.
func Select(avail []Utxo, need uint64) []Utxo {
	sorted := slices.Clone(avail)
	slices.SortFunc(sorted, func(a, b Utxo) int {
		return cmp.Or(cmp.Compare(b.Lovelace, a.Lovelace), compareRefs(a.Ref, b.Ref))
	})
	var sum uint64
	for i, u := range sorted {
		sum += u.Lovelace
		if sum >= need {
			return slices.Clip(sorted[:i+1])
		}
	}
	return nil
}
