package build

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
)

// runs completes tx's redeemer list with what evaluation gave: the units
// each redeemer used, or the evaluation error. gouroboros evaluates the
// transaction as a whole and stops at the first failing script, so an
// error is every run's: exact for one redeemer, the transaction's verdict
// for several.
func runs(tx common.Transaction, utxos []common.Utxo, used map[common.RedeemerKey]common.ExUnits, evalErr error) []contract.Run {
	byRef := make(map[string]common.TransactionOutput, len(utxos))
	for _, u := range utxos {
		byRef[u.Id.String()] = u.Output
	}

	resolve := func(in common.TransactionInput) (common.TransactionOutput, bool) {
		o, ok := byRef[in.String()]
		return o, ok
	}
	out := contract.Redeemers(tx, resolve, name)

	for i := range out {
		r := &out[i]
		if evalErr != nil {
			r.Error = evalErr.Error()
			continue
		}

		tag, _ := r.Tag()
		u := used[common.RedeemerKey{Tag: tag, Index: r.Index}]
		r.Used = contract.Units{Mem: u.Memory, Steps: u.Steps}
		r.OK = u.Memory <= r.Declared.Mem && u.Steps <= r.Declared.Steps
		if !r.OK {
			r.Error = "declared units below the units used"
		}
	}
	return out
}

// name names a script hash by the scripts the builder has made.
func name(h common.Blake2b224) (validator, instance string, ok bool) {
	s, found := Lookup(h)
	if !found {
		return contract.Registered(h)
	}

	if s.Name != s.Validator {
		instance = s.Name
	}
	return s.Validator, instance, true
}
