package plan

import (
	"fmt"
	"maps"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// outputs are the owned outputs unspent at the settled point: the model's
// Utxo of the settled prefix. byOwner indexes them by owner, so a balance
// query reads one actor's outputs instead of scanning all of them. Only put
// and drop write either map, keeping the two in step; indexConsistent
// compares them with a scan.
type outputs struct {
	utxos   map[Ref]Utxo
	byOwner map[population.ActorID]map[Ref]struct{}
}

func newOutputs() outputs {
	return outputs{utxos: map[Ref]Utxo{}, byOwner: map[population.ActorID]map[Ref]struct{}{}}
}

func (o *outputs) clone() outputs {
	c := outputs{utxos: maps.Clone(o.utxos), byOwner: make(map[population.ActorID]map[Ref]struct{}, len(o.byOwner))}
	for a, m := range o.byOwner {
		c.byOwner[a] = maps.Clone(m)
	}
	return c
}

func (o *outputs) put(u Utxo) {
	o.utxos[u.Ref] = u
	m := o.byOwner[u.Owner]
	if m == nil {
		m = map[Ref]struct{}{}
		o.byOwner[u.Owner] = m
	}
	m[u.Ref] = struct{}{}
}

func (o *outputs) drop(r Ref) {
	u, ok := o.utxos[r]
	if !ok {
		return
	}
	delete(o.utxos, r)
	delete(o.byOwner[u.Owner], r)
}

// indexConsistent reports whether byOwner holds exactly what utxos implies.
func (o *outputs) indexConsistent() error {
	n := 0
	for a, m := range o.byOwner {
		for r := range m {
			if u, ok := o.utxos[r]; !ok || u.Owner != a {
				return fmt.Errorf("owner index lists %x#%d under actor %d", r.Tx[:4], r.Index, a)
			}
			n++
		}
	}
	if n != len(o.utxos) {
		return fmt.Errorf("owner index has %d outputs, utxos %d", n, len(o.utxos))
	}
	return nil
}
