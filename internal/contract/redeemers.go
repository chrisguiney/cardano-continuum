package contract

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/common/script"
	"github.com/blinklabs-io/plutigo/data"
)

// Resolver gives the output an input spends, if the caller knows it.
type Resolver func(common.TransactionInput) (common.TransactionOutput, bool)

// Namer names the validator and instance a script hash belongs to, if the
// caller knows it.
type Namer func(common.Blake2b224) (validator, instance string, ok bool)

// tagPurposes maps redeemer tags to purposes. Cert, reward, voting and
// proposing are the ledger's names for publish, withdraw, vote and propose.
var tagPurposes = map[common.RedeemerTag]Purpose{
	common.RedeemerTagSpend:     Spend,
	common.RedeemerTagMint:      Mint,
	common.RedeemerTagCert:      Publish,
	common.RedeemerTagReward:    Withdraw,
	common.RedeemerTagVoting:    Vote,
	common.RedeemerTagProposing: Propose,
}

// Redeemers lists the script runs tx asks for, one per redeemer in tag
// then index order, with the declared units and nothing evaluated: Used,
// OK and Error are left for an evaluator. The script hash is found where
// the ledger finds it: a spend's index points into the sorted inputs, whose
// resolved address names the script; a mint's into the sorted policy ids.
// Other purposes, and spends of inputs resolve does not know, have no hash.
func Redeemers(tx common.Transaction, resolve Resolver, name Namer) []Run {
	ws := tx.Witnesses()
	if ws == nil || ws.Redeemers() == nil {
		return nil
	}

	var out []Run
	for k, v := range ws.Redeemers().Iter() {
		r := Run{
			Purpose:  cmp.Or(tagPurposes[k.Tag], Purpose("tag"+strconv.Itoa(int(k.Tag)))),
			Index:    k.Index,
			Declared: Units{Mem: v.ExUnits.Memory, Steps: v.ExUnits.Steps},
		}
		if v.Data.Data != nil {
			r.Redeemer, r.RedeemerJSON = encode(v.Data.Data)
		}

		if h, datum, ok := target(tx, k, resolve); ok {
			r.Script = h.String()
			if datum != nil {
				r.Datum, r.DatumJSON = encode(datum)
			}
			if name != nil {
				r.Validator, r.Instance, _ = name(h)
			}
		}
		out = append(out, r)
	}

	slices.SortFunc(out, func(a, b Run) int {
		return cmp.Or(cmp.Compare(tagOf(a.Purpose), tagOf(b.Purpose)), cmp.Compare(a.Index, b.Index))
	})
	return out
}

func tagOf(p Purpose) int {
	for t, q := range tagPurposes {
		if q == p {
			return int(t)
		}
	}
	return 1 << 8
}

// target is the script hash redeemer k runs and, for a spend, the datum of
// the output it spends.
func target(tx common.Transaction, k common.RedeemerKey, resolve Resolver) (common.Blake2b224, data.PlutusData, bool) {
	switch k.Tag {
	case common.RedeemerTagSpend:
		ins := script.SortInputs(tx.Inputs())
		if int(k.Index) >= len(ins) || resolve == nil {
			return common.Blake2b224{}, nil, false
		}

		o, ok := resolve(ins[k.Index])
		if !ok || o.Address().Type()&common.AddressTypeScriptBit == 0 {
			return common.Blake2b224{}, nil, false
		}

		var datum data.PlutusData
		if d := o.Datum(); d != nil {
			datum = d.Data
		}
		return o.Address().PaymentKeyHash(), datum, true
	case common.RedeemerTagMint:
		m := tx.AssetMint()
		if m == nil {
			return common.Blake2b224{}, nil, false
		}

		ps := m.Policies()
		slices.SortFunc(ps, func(a, b common.Blake2b224) int { return bytes.Compare(a[:], b[:]) })
		if int(k.Index) >= len(ps) {
			return common.Blake2b224{}, nil, false
		}
		return ps[k.Index], nil, true
	}
	return common.Blake2b224{}, nil, false
}

// encode renders d as CBOR hex and detailed-schema JSON; either is empty
// if it does not encode.
func encode(d data.PlutusData) (cborHex, js string) {
	if raw, err := data.Encode(d); err == nil {
		cborHex = hex.EncodeToString(raw)
	}

	if j, err := data.EncodeJSON(d); err == nil {
		js = string(j)
	}
	return cborHex, js
}

// Registered names a script hash by the registry's unapplied validators:
// enough for an unparameterised validator, and a Namer for callers that
// know no instances.
func Registered(h common.Blake2b224) (validator, instance string, ok bool) {
	for _, v := range registry {
		if v.Hash == h {
			return v.Name, "", true
		}
	}
	return "", "", false
}
