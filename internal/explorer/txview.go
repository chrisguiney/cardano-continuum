package explorer

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
)

// resolver gives the output an input spends, if the index knows it.
type resolver func(common.TransactionInput) (common.TransactionOutput, bool)

// txFee is the lovelace the transaction moves to the fee pot: its fee, or for
// an invalid transaction its forfeited collateral (as chain.TxFrom).
func txFee(tx common.Transaction) uint64 {
	if tx.IsValid() {
		if f := tx.Fee(); f != nil {
			return f.Uint64()
		}
		return 0
	}
	if c := tx.TotalCollateral(); c != nil {
		return c.Uint64()
	}
	return 0
}

// exUnits sums the declared execution budgets of tx's redeemers.
func exUnits(tx common.Transaction) (mem, steps uint64) {
	ws := tx.Witnesses()
	if ws == nil || ws.Redeemers() == nil {
		return 0, 0
	}
	for _, v := range ws.Redeemers().Iter() {
		mem += uint64(max(v.ExUnits.Memory, 0))
		steps += uint64(max(v.ExUnits.Steps, 0))
	}
	return mem, steps
}

// txView renders tx in full. Pure: resolve is the only lookup.
func txView(tx common.Transaction, resolve resolver) TxView {
	v := TxView{
		Hash: tx.Hash().String(), Valid: tx.IsValid(), Size: len(tx.Cbor()), Fee: txFee(tx), CborHex: hex.EncodeToString(tx.Cbor()),
		Inputs: inputViews(tx.Inputs(), resolve), RefInputs: inputViews(tx.ReferenceInputs(), resolve),
		Collateral: inputViews(tx.Collateral(), resolve),
		Outputs:    []OutputView{}, Mint: []AssetView{}, Certs: []CertView{}, Withdrawals: []WithdrawalView{}, Signers: []string{},
		Redeemers: []RedeemerView{}, Scripts: []ScriptView{}, Votes: []VoteView{}, Proposals: []ProposalView{},
	}
	id := v.Hash
	for i, o := range tx.Outputs() {
		v.Outputs = append(v.Outputs, outputView(id, uint32(i), o))
	}
	if cr := tx.CollateralReturn(); cr != nil && !reflect.ValueOf(cr).IsNil() {
		ov := outputView(id, uint32(len(tx.Outputs())), cr)
		v.CollateralReturn = &ov
	}
	if t := tx.TotalCollateral(); t != nil && t.Sign() > 0 {
		u := t.Uint64()
		v.TotalCollateral = &u
	}
	v.TTL, v.ValidFrom = interval(tx)
	if m := assetViews(tx.AssetMint()); m != nil {
		v.Mint = m
	}
	for _, c := range tx.Certificates() {
		v.Certs = append(v.Certs, certView(c))
	}
	for a, amt := range tx.Withdrawals() {
		v.Withdrawals = append(v.Withdrawals, WithdrawalView{Address: a.String(), Lovelace: bigU64(amt)})
	}
	slices.SortFunc(v.Withdrawals, func(a, b WithdrawalView) int { return strings.Compare(a.Address, b.Address) })
	for _, s := range tx.RequiredSigners() {
		v.Signers = append(v.Signers, s.String())
	}
	v.Metadata = metadataView(tx.Metadata())
	v.Votes = voteViews(tx.VotingProcedures())
	v.Proposals = proposalViews(tx.ProposalProcedures())
	witnessViews(&v, tx.Witnesses())
	targetViews(v.Redeemers, contract.Redeemers(tx, contract.Resolver(resolve), contract.Registered))
	return v
}

// targetViews fills in each redeemer's purpose, script and datum from
// runs, both in tag-then-index order.
func targetViews(rs []RedeemerView, runs []contract.Run) {
	for i := range rs {
		j := slices.IndexFunc(runs, func(r contract.Run) bool { return redeemerTag(tagOf(r)) == rs[i].Tag && r.Index == rs[i].Index })
		if j < 0 {
			continue
		}

		r := runs[j]
		rs[i].Purpose, rs[i].Script, rs[i].Validator = string(r.Purpose), r.Script, r.Validator
		rs[i].DataJSON, rs[i].DatumHex, rs[i].DatumJSON = r.RedeemerJSON, r.Datum, r.DatumJSON
	}
}

func tagOf(r contract.Run) common.RedeemerTag {
	t, _ := r.Tag()
	return t
}

func inputViews(ins []common.TransactionInput, resolve resolver) []InputView {
	out := make([]InputView, 0, len(ins))
	for _, in := range ins {
		iv := InputView{Tx: in.Id().String(), Index: in.Index()}
		if o, ok := resolve(in); ok {
			ov := outputView(iv.Tx, iv.Index, o)
			iv.Resolved = &ov
		}
		out = append(out, iv)
	}
	return out
}

func interval(tx common.Transaction) (ttl, from *uint64) {
	if ub, ok := tx.(common.TransactionWithValidityIntervalUpperBound); ok {
		if v, has := ub.ValidityIntervalUpperBound(); has {
			ttl = &v
		}
	} else if t := tx.TTL(); t != 0 {
		ttl = &t
	}
	if lb, ok := tx.(common.TransactionWithValidityIntervalLowerBound); ok {
		if v, has := lb.ValidityIntervalLowerBound(); has {
			from = &v
		}
	} else if s := tx.ValidityIntervalStart(); s != 0 {
		from = &s
	}
	return ttl, from
}

func bigU64(b *big.Int) uint64 {
	if b == nil {
		return 0
	}
	return b.Uint64()
}

func witnessViews(v *TxView, ws common.TransactionWitnessSet) {
	if ws == nil {
		return
	}
	if rs := ws.Redeemers(); rs != nil {
		for k, r := range rs.Iter() {
			rv := RedeemerView{Tag: redeemerTag(k.Tag), Index: k.Index, Mem: uint64(max(r.ExUnits.Memory, 0)), Steps: uint64(max(r.ExUnits.Steps, 0))}
			if r.Data.Data != nil {
				rv.DataHex = hex.EncodeToString(r.Data.Cbor())
			}
			v.Redeemers = append(v.Redeemers, rv)
		}
		slices.SortFunc(v.Redeemers, func(a, b RedeemerView) int {
			if c := strings.Compare(a.Tag, b.Tag); c != 0 {
				return c
			}
			return int(a.Index) - int(b.Index)
		})
	}
	for _, s := range ws.NativeScripts() {
		v.Scripts = append(v.Scripts, scriptView(s))
	}
	for _, s := range ws.PlutusV1Scripts() {
		v.Scripts = append(v.Scripts, scriptView(s))
	}
	for _, s := range ws.PlutusV2Scripts() {
		v.Scripts = append(v.Scripts, scriptView(s))
	}
	for _, s := range ws.PlutusV3Scripts() {
		v.Scripts = append(v.Scripts, scriptView(s))
	}
}

func redeemerTag(t common.RedeemerTag) string {
	names := []string{"spend", "mint", "cert", "reward", "voting", "proposing", "guarding"}
	if int(t) < len(names) {
		return names[t]
	}
	return "tag" + strconv.Itoa(int(t))
}

var voterNames = []string{"cc_key", "cc_script", "drep_key", "drep_script", "pool"}
var choices = []string{"no", "yes", "abstain"}

func voteViews(vps common.VotingProcedures) []VoteView {
	out := []VoteView{}
	for voter, acts := range vps {
		for id, vp := range acts {
			vv := VoteView{VoterHash: hex.EncodeToString(voter.Hash[:]), ActionTx: hex.EncodeToString(id.TransactionId[:]), ActionIndex: id.GovActionIdx, Voter: "voter" + strconv.Itoa(int(voter.Type)), Choice: "vote" + strconv.Itoa(int(vp.Vote))}
			if int(voter.Type) < len(voterNames) {
				vv.Voter = voterNames[voter.Type]
			}
			if int(vp.Vote) < len(choices) {
				vv.Choice = choices[vp.Vote]
			}
			out = append(out, vv)
		}
	}
	slices.SortFunc(out, func(a, b VoteView) int {
		return strings.Compare(a.ActionTx+a.VoterHash+strconv.Itoa(int(a.ActionIndex)), b.ActionTx+b.VoterHash+strconv.Itoa(int(b.ActionIndex)))
	})
	return out
}

func proposalViews(ps []common.ProposalProcedure) []ProposalView {
	out := []ProposalView{}
	for i, p := range ps {
		pv := ProposalView{Index: i, Deposit: p.Deposit(), Return: p.RewardAccount().String(), Kind: snake(reflect.TypeOf(p.GovAction()).String(), "GovAction")}
		a := p.Anchor()
		pv.Anchor = a.Url
		out = append(out, pv)
	}
	return out
}

// metadataView renders auxiliary-data metadata as JSON-shaped values: label
// to value, bytes as 0x-prefixed hex, integers that exceed 2^53 as strings.
func metadataView(m common.TransactionMetadatum) map[string]any {
	if m == nil || reflect.ValueOf(m).IsZero() {
		return nil
	}
	mm, ok := m.(common.MetaMap)
	if !ok {
		if p, isPtr := m.(*common.MetaMap); isPtr && p != nil {
			mm = *p
		} else {
			return map[string]any{"value": metaValue(m, 0)}
		}
	}
	out := map[string]any{}
	for _, pr := range mm.Pairs {
		out[metaKey(pr.Key)] = metaValue(pr.Value, 0)
	}
	return out
}

const maxMetaDepth = 32

func metaKey(k common.TransactionMetadatum) string {
	switch x := k.(type) {
	case common.MetaInt:
		return x.Value.String()
	case common.MetaText:
		return x.Value
	case common.MetaBytes:
		return "0x" + hex.EncodeToString(x.Value)
	}
	return fmt.Sprintf("<%s>", k.TypeName())
}

func metaValue(m common.TransactionMetadatum, depth int) any {
	if depth > maxMetaDepth {
		return "<too deep>"
	}
	switch x := m.(type) {
	case common.MetaInt:
		if x.Value.IsInt64() && x.Value.Int64() < 1<<53 && x.Value.Int64() > -(1<<53) {
			return x.Value.Int64()
		}
		return x.Value.String()
	case common.MetaText:
		return x.Value
	case common.MetaBytes:
		return "0x" + hex.EncodeToString(x.Value)
	case common.MetaList:
		l := make([]any, 0, len(x.Items))
		for _, it := range x.Items {
			l = append(l, metaValue(it, depth+1))
		}
		return l
	case common.MetaMap:
		o := map[string]any{}
		for _, pr := range x.Pairs {
			o[metaKey(pr.Key)] = metaValue(pr.Value, depth+1)
		}
		return o
	}
	return nil
}
