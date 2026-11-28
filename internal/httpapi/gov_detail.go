package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
)

// GovDetail is /api/governance/{tx}/{index}: the proposal as the chain
// view folded it (status, expiry, tallies), what the proposal procedure
// asks for as the explorer decoded it, who may vote and every current vote.
type GovDetail struct {
	ID       string           `json:"id"`
	Epoch    *uint64          `json:"epoch,omitempty"`
	Lifetime uint64           `json:"lifetime_epochs"`
	Proposal view.ProposalRow `json:"proposal"`
	Summary  string           `json:"summary,omitempty"`
	Content  *GovContent      `json:"content,omitempty"`
	Voters   []VoterRule      `json:"voters"`
	Votes    []GovVote        `json:"votes"`
	// Treasury is the treasury now as the engine's chain view reckons it,
	// which a withdrawal draws on; absent before the engine reported one.
	Treasury *uint64 `json:"treasury_lovelace,omitempty"`
}

// GovContent is the decoded action with the continuum actors behind its
// addresses.
type GovContent struct {
	explorer.GovAction
	ReturnActor *ActorRef  `json:"return_actor,omitempty"`
	Withdrawals []GovPayee `json:"withdrawals,omitempty"`
}

// GovPayee is a treasury withdrawal's recipient, with its actor if the run
// owns the address.
type GovPayee struct {
	explorer.Withdrawal
	Actor *ActorRef `json:"actor,omitempty"`
}

// VoterRule says whether a voter class may vote on the action and why.
type VoterRule struct {
	Class   string `json:"class"`
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	Rule    string `json:"rule"`
}

// GovVote is a current vote with the anchor it carried, when the explorer
// still knows it.
type GovVote struct {
	view.VoteRow
	Anchor *explorer.Anchor `json:"anchor,omitempty"`
}

// govRoutes serves the governance action detail.
type govRoutes struct{ *api }

// register adds GET /api/governance/{tx}/{index}.
func (g govRoutes) register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/governance/{tx}/{index}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := exQuery(r); err != nil {
			g.fail(w, err)
			return
		}
		tx := r.PathValue("tx")
		idx, err := strconv.ParseUint(r.PathValue("index"), 10, 32)
		if err != nil || !reHash64.MatchString(tx) {
			g.fail(w, badf("a governance action is a 64-digit transaction hash and an index"))
			return
		}
		d, err := govDetail(r.Context(), g.Source, g.Explorer, tx, uint32(idx))
		if err != nil {
			g.fail(w, err)
			return
		}
		g.json(w, http.StatusOK, d)
	})
}

func govDetail(ctx context.Context, src Source, ex *Explorer, tx string, idx uint32) (GovDetail, error) {
	id := fmt.Sprintf("%s#%d", hexLower(tx), idx)
	pd, err := src.Proposal(ctx, id)
	if err != nil {
		return GovDetail{}, err
	}
	d := GovDetail{ID: id, Epoch: pd.Epoch, Lifetime: pd.Lifetime, Proposal: pd.Row, Voters: voterRules(pd.Row.Type, pd.Row.PoolsMayVote), Votes: make([]GovVote, 0, len(pd.Votes)), Treasury: pd.Treasury}
	var anchors []explorer.VoteAnchor
	if ex != nil {
		anchors = ex.idx.GovVotes(tx, idx)
		if d.Content, d.Summary, err = govContent(ctx, ex, tx, idx); err != nil {
			return GovDetail{}, err
		}
	}
	for _, v := range pd.Votes {
		gv := GovVote{VoteRow: v}
		if i := slices.IndexFunc(anchors, func(a explorer.VoteAnchor) bool { return a.Tx == v.Tx && a.VoterHash == v.VoterID }); i >= 0 {
			gv.Anchor = &anchors[i].Anchor
		}
		d.Votes = append(d.Votes, gv)
	}
	return d, nil
}

// govContent decodes the action and names the actors behind its addresses.
// An action the index does not hold is no content, not an error: the fold
// can know a proposal the index never saw.
func govContent(ctx context.Context, ex *Explorer, tx string, idx uint32) (*GovContent, string, error) {
	a, err := ex.idx.GovAction(tx, idx)
	if err != nil {
		return nil, "", nil
	}
	a = explorer.WithCurrent(a, ex.opt.Params)
	c := &GovContent{GovAction: a}
	if c.ReturnActor, err = ex.actors.ofAddress(ctx, a.Return); err != nil {
		return nil, "", err
	}
	for _, w := range a.Withdrawals {
		p := GovPayee{Withdrawal: w}
		if p.Actor, err = ex.actors.ofAddress(ctx, w.Address); err != nil {
			return nil, "", err
		}
		c.Withdrawals = append(c.Withdrawals, p)
	}
	return c, explorer.Summary(a), nil
}

// fillSummaries writes each proposal's one-line summary from the explorer's
// decoding. A proposal the index does not hold keeps none.
func fillSummaries(ex *Explorer, g *view.GovernanceView) {
	for i := range g.Proposals {
		tx, idx, ok := splitID(g.Proposals[i].ID)
		if !ok {
			continue
		}
		if a, err := ex.idx.GovAction(tx, idx); err == nil {
			g.Proposals[i].Summary = explorer.Summary(explorer.WithCurrent(a, ex.opt.Params))
		}
	}
}

func splitID(id string) (string, uint32, bool) {
	n := len(id) - 1
	for n >= 0 && id[n] != '#' {
		n--
	}
	if n < 0 {
		return "", 0, false
	}
	idx, err := strconv.ParseUint(id[n+1:], 10, 32)
	return id[:n], uint32(idx), err == nil
}

func hexLower(s string) string {
	b, err := hex.DecodeString(s)
	if err != nil {
		return s
	}
	return hex.EncodeToString(b)
}

// voterRules is the eligibility of each voter class for an action of type
// typ; pools is the proposal's spo_may_vote. The sets are chain.MayVote's.
func voterRules(typ string, pools bool) []VoterRule {
	drep, spo, cc := chain.MayVote(typ, pools)
	return []VoterRule{
		{Class: "drep", Allowed: drep, Reason: drepReason(drep), Rule: "isDRepVotingAllowed, Governance/Internal.hs"},
		{Class: "spo", Allowed: spo, Reason: spoReason(typ, spo), Rule: "isStakePoolVotingAllowed, Governance/Internal.hs"},
		{Class: "committee", Allowed: cc, Reason: ccReason(typ, cc), Rule: "isCommitteeVotingAllowed, Governance/Internal.hs"},
	}
}

func drepReason(ok bool) string {
	if !ok {
		return "An action of unknown type has no voters."
	}
	return "DReps vote on every kind of governance action."
}

func spoReason(typ string, ok bool) string {
	switch {
	case typ == "":
		return "An action of unknown type has no voters."
	case typ == "parameter_change" && ok:
		return "The change sets a security-group parameter, which stake pools help protect."
	case typ == "parameter_change":
		return "The change sets no security-group parameter, so stake pools do not vote."
	case ok:
		return "Stake pools vote on no-confidence, committee updates, hard forks and info actions."
	case typ == "treasury_withdrawals":
		return "Stake pools do not vote on treasury withdrawals."
	}
	return "Stake pools do not vote on constitution changes."
}

func ccReason(typ string, ok bool) string {
	switch {
	case typ == "":
		return "An action of unknown type has no voters."
	case ok:
		return "The committee votes on everything except no-confidence and committee updates."
	}
	return "The committee may not vote on its own removal or replacement."
}
