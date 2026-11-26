package explorer

import (
	"encoding/hex"
	"math/big"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// outputView renders a ledger output. Pure.
func outputView(tx string, idx uint32, o common.TransactionOutput) OutputView {
	v := OutputView{Tx: tx, Index: idx, Address: o.Address().String()}
	if a := o.Amount(); a != nil {
		v.Lovelace = a.Uint64()
	}
	v.Assets = assetViews(o.Assets())
	switch d := o.Datum(); {
	case d != nil:
		v.Datum = &DatumView{Kind: "inline", Hash: d.Hash().String(), Hex: hex.EncodeToString(d.Cbor())}
	case o.DatumHash() != nil:
		v.Datum = &DatumView{Kind: "hash", Hash: o.DatumHash().String()}
	}
	if s := o.ScriptRef(); s != nil {
		sv := scriptView(s)
		v.ScriptRef = &sv
	}
	return v
}

func assetViews[T int64 | uint64 | *big.Int](ma *common.MultiAsset[T]) []AssetView {
	if ma == nil {
		return nil
	}
	var out []AssetView
	for _, p := range ma.Policies() {
		for _, n := range ma.Assets(p) {
			out = append(out, assetView(p, n, any(ma.Asset(p, n))))
		}
	}
	sortAssets(out)
	return out
}

func assetView(p common.Blake2b224, name []byte, q any) AssetView {
	a := AssetView{Policy: p.String(), Name: hex.EncodeToString(name), Quantity: quantityString(q)}
	if s := string(name); utf8.ValidString(s) && isPrintable(s) {
		a.NameText = s
	}
	return a
}

func quantityString(q any) string {
	switch v := q.(type) {
	case *big.Int:
		if v == nil {
			return "0"
		}
		return v.String()
	case int64:
		return big.NewInt(v).String()
	case uint64:
		return new(big.Int).SetUint64(v).String()
	}
	return "0"
}

func isPrintable(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func sortAssets(a []AssetView) {
	slices.SortFunc(a, func(x, y AssetView) int {
		if c := strings.Compare(x.Policy, y.Policy); c != 0 {
			return c
		}
		return strings.Compare(x.Name, y.Name)
	})
}

// scriptView names a script by hash and language.
func scriptView(s common.Script) ScriptView {
	return ScriptView{Hash: s.Hash().String(), Language: languageOf(s), Size: len(s.RawScriptBytes())}
}

func languageOf(s common.Script) string {
	switch s.(type) {
	case common.PlutusV1Script, *common.PlutusV1Script:
		return "plutus_v1"
	case common.PlutusV2Script, *common.PlutusV2Script:
		return "plutus_v2"
	case common.PlutusV3Script, *common.PlutusV3Script:
		return "plutus_v3"
	case common.NativeScript, *common.NativeScript:
		return "native"
	}
	return "unknown"
}
