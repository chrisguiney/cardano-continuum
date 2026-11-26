package mistake

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
)

// overDeclare raises the redeemers' declared memory to overDeclared in
// total, putting the excess on the first one; steps are left as measured.
func overDeclare(m *mutant) error {
	target := overDeclared(m.c.Params)
	if m.c.Params.MaxBlockExUnits.Memory <= 0 {
		return fmt.Errorf("%w: no block budget to exceed", ErrNotApplicable)
	}
	var total uint64
	if err := m.redeemers(func(r *redeemer) { total += r.mem }); err != nil {
		return err
	}
	if total >= uint64(target) {
		return fmt.Errorf("%w: already declares %d memory", ErrNotApplicable, total)
	}
	first := true
	return m.redeemers(func(r *redeemer) {
		if first {
			r.mem += uint64(target) - total
			first = false
		}
	})
}

// isInvalid is the CBOR false of a transaction's is_valid field.
var isInvalid = cbor.RawMessage{0xf4}

// underDeclare declares a tenth of every redeemer's units, below what its
// script spends, and flags the transaction phase-2 invalid, as a wallet
// that knows its budget fails and submits anyway would.
func underDeclare(m *mutant) error {
	if err := m.redeemers(func(r *redeemer) {
		r.mem /= exUnitsDivisor
		r.steps /= exUnitsDivisor
	}); err != nil {
		return err
	}
	m.rest[0] = isInvalid
	return nil
}
