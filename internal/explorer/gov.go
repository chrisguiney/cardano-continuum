package explorer

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// Anchor is a URL with the hash of the document it names.
type Anchor struct {
	URL  string `json:"url"`
	Hash string `json:"hash"`
}

// ActionRef names a governance action: the transaction that proposed it and
// its index there.
type ActionRef struct {
	Tx    string `json:"tx"`
	Index uint32 `json:"index"`
}

// Withdrawal is one treasury withdrawal: the reward address that receives
// it and the lovelace.
type Withdrawal struct {
	Address  string `json:"address"`
	Lovelace uint64 `json:"lovelace"`
}

// Member is a constitutional committee member by credential
// ("key:<hash>" or "script:<hash>"), with the epoch its term ends for a
// member an action adds.
type Member struct {
	Credential string `json:"credential"`
	Epoch      uint64 `json:"epoch,omitempty"`
}

// Constitution is a new constitution's anchor and its guardrails script,
// empty when the constitution has none.
type Constitution struct {
	Anchor     Anchor `json:"anchor"`
	ScriptHash string `json:"script_hash,omitempty"`
}

// Version is a protocol version.
type Version struct {
	Major uint `json:"major"`
	Minor uint `json:"minor"`
}

// GovAction is a governance action decoded from its proposal procedure.
// Kind is one of the names chain.ActionTypes lists, or the type's own name
// for an action this decoder does not know. Only the fields of its Kind are
// set.
type GovAction struct {
	Tx     string `json:"tx"`
	Index  uint32 `json:"index"`
	Slot   uint64 `json:"slot"`
	Kind   string `json:"kind"`
	Return string `json:"return_address"`
	// Deposit is the lovelace the proposer locked.
	Deposit uint64  `json:"deposit"`
	Anchor  *Anchor `json:"anchor,omitempty"`
	// Prev is the action this one builds on, nil when it names none.
	Prev *ActionRef `json:"prev_action,omitempty"`

	Params     []ParamChange `json:"params,omitempty"`
	PolicyHash string        `json:"policy_hash,omitempty"`

	Withdrawals []Withdrawal `json:"withdrawals,omitempty"`
	Total       uint64       `json:"total,omitempty"`

	Removed []string `json:"removed,omitempty"`
	Added   []Member `json:"added,omitempty"`
	Quorum  string   `json:"quorum,omitempty"`

	Constitution *Constitution `json:"constitution,omitempty"`
	Version      *Version      `json:"version,omitempty"`
}

// govDecode reads a proposal procedure. Pure; the transaction and slot are
// the caller's to set.
func govDecode(p common.ProposalProcedure) GovAction {
	g := GovAction{Return: p.RewardAccount().String(), Deposit: p.Deposit()}
	if a := p.Anchor(); a.Url != "" || a.DataHash != ([32]byte{}) {
		g.Anchor = anchorOf(a)
	}
	switch x := p.GovAction().(type) {
	case *conway.ConwayParameterChangeGovAction:
		g.Kind, g.Prev, g.PolicyHash = "parameter_change", actionRef(x.ActionId), hexOf(x.PolicyHash)
		g.Params = paramChanges(reflect.ValueOf(x.ParamUpdate), x.SecurityGroupFields())
	case *common.TreasuryWithdrawalGovAction:
		g.Kind, g.PolicyHash = "treasury_withdrawals", hexOf(x.PolicyHash)
		g.Withdrawals, g.Total = withdrawals(x.Withdrawals)
	case *common.HardForkInitiationGovAction:
		g.Kind, g.Prev = "hard_fork_initiation", actionRef(x.ActionId)
		g.Version = &Version{Major: x.ProtocolVersion.Major, Minor: x.ProtocolVersion.Minor}
	case *common.NoConfidenceGovAction:
		g.Kind, g.Prev = "no_confidence", actionRef(x.ActionId)
	case *common.UpdateCommitteeGovAction:
		g.Kind, g.Prev = "update_committee", actionRef(x.ActionId)
		g.Removed, g.Added, g.Quorum = committee(x)
	case *common.NewConstitutionGovAction:
		g.Kind, g.Prev = "new_constitution", actionRef(x.ActionId)
		g.Constitution = &Constitution{Anchor: *anchorOf(x.Constitution.Anchor), ScriptHash: hexOf(x.Constitution.ScriptHash)}
	case *common.InfoGovAction:
		g.Kind = "info"
	default:
		g.Kind = snake(reflect.TypeOf(x).String(), "GovAction")
	}
	return g
}

// WithCurrent returns g with each parameter change's present value read
// from cur, a conway.ConwayProtocolParameters or a pointer to one. g is
// unchanged for any other action.
func WithCurrent(g GovAction, cur *conway.ConwayProtocolParameters) GovAction {
	if cur != nil {
		g.Params = setCurrent(g.Params, reflect.ValueOf(cur))
	}
	return g
}

func anchorOf(a common.GovAnchor) *Anchor {
	return &Anchor{URL: a.Url, Hash: hex.EncodeToString(a.DataHash[:])}
}

func hexOf(b []byte) string { return hex.EncodeToString(b) }

func actionRef(id *common.GovActionId) *ActionRef {
	if id == nil {
		return nil
	}
	return &ActionRef{Tx: hex.EncodeToString(id.TransactionId[:]), Index: id.GovActionIdx}
}

func withdrawals(m map[*common.Address]uint64) ([]Withdrawal, uint64) {
	out := make([]Withdrawal, 0, len(m))
	var total uint64
	for a, v := range m {
		out = append(out, Withdrawal{Address: a.String(), Lovelace: v})
		total += v
	}
	slices.SortFunc(out, func(a, b Withdrawal) int { return strings.Compare(a.Address, b.Address) })
	return out, total
}

func committee(x *common.UpdateCommitteeGovAction) (removed []string, added []Member, quorum string) {
	for _, c := range x.Credentials {
		removed = append(removed, credText(c))
	}
	for c, e := range x.CredEpochs {
		added = append(added, Member{Credential: credText(*c), Epoch: e})
	}
	slices.Sort(removed)
	slices.SortFunc(added, func(a, b Member) int { return strings.Compare(a.Credential, b.Credential) })
	if x.Quorum.Rat != nil {
		quorum = x.Quorum.RatString()
	}
	return removed, added, quorum
}

func credText(c common.Credential) string {
	if c.CredType == common.CredentialTypeScriptHash {
		return "script:" + credHex(c)
	}
	return "key:" + credHex(c)
}

// Summary is a one-line description of what g asks for.
func Summary(g GovAction) string {
	switch g.Kind {
	case "parameter_change":
		return paramSummary(g.Params)
	case "treasury_withdrawals":
		return fmt.Sprintf("Withdraw %s ADA to %s", ada(g.Total), plural(len(g.Withdrawals), "recipient"))
	case "update_committee":
		return fmt.Sprintf("Update committee: add %d, remove %d, quorum %s", len(g.Added), len(g.Removed), g.Quorum)
	case "new_constitution":
		if g.Constitution != nil && g.Constitution.Anchor.URL != "" {
			return "New constitution (" + g.Constitution.Anchor.URL + ")"
		}
		return "New constitution"
	case "hard_fork_initiation":
		if g.Version != nil {
			return fmt.Sprintf("Hard fork to protocol version %d.%d", g.Version.Major, g.Version.Minor)
		}
		return "Hard fork"
	case "no_confidence":
		return "No confidence in the committee"
	case "info":
		return "Info action: a poll with no effect"
	}
	return g.Kind
}

func paramSummary(ps []ParamChange) string {
	if len(ps) == 0 {
		return "Change no parameter"
	}
	p := ps[0]
	s := "Change " + p.Name + " to " + p.newB + p.sfx
	if p.curB != "" {
		s = "Change " + p.Name + " " + p.curB + "→" + p.newB + p.sfx
	}
	if n := len(ps) - 1; n > 0 {
		s += "; " + strconv.Itoa(n) + " other " + map[bool]string{true: "param", false: "params"}[n == 1]
	}
	return s
}

// ada writes lovelace as ADA, with the fraction only when there is one.
func ada(l uint64) string {
	whole, frac := l/1_000_000, l%1_000_000
	s := grouped(whole)
	if frac == 0 {
		return s
	}
	return s + "." + strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
