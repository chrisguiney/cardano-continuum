package grants

import (
	"math"
	"math/rand/v2"
	"slices"
)

// ProfilePrefix starts every recipient's profile name.
const ProfilePrefix = "recipient-"

// MainnetTreasury is mainnet's treasury in lovelace at epoch 670 (Koios
// totals), the treasury the fitted amounts were asked of.
const MainnetTreasury = 1_433_845_938_623_351

// maxShare caps one action's total ask as a share of the treasury: the
// largest mainnet ask was about 8% of it.
const maxShare = 0.1

// bundleShare is the share of actions that pay several recipients, and
// maxBundle the most an action pays. Mainnet's actions nearly all pay
// one payee (2 of 111 paid five); bundles are drawn more often so a run
// exercises multi-recipient enactment, which is not fitted.
const (
	bundleShare = 0.25
	maxBundle   = 4
)

// Kind is one recipient type.
type Kind struct {
	// Name is the type as people read it ("tooling developer").
	Name string
	// Profile is the recipient actor's profile name.
	Profile string
	// Share is the type's share of mainnet's payees.
	Share float64
	// Mu and Sigma are the lognormal fit of the type's asks, in ln ADA.
	Mu, Sigma float64
	// Milestones means the recipient locks its award for milestone draws
	// under the governance administrator's oversight instead of taking it
	// at once.
	Milestones bool
}

// kinds are the recipient types, in a fixed order. Node implementers'
// fitted share is split between three teams.
var kinds = []Kind{
	{Name: "node implementer A", Profile: ProfilePrefix + "node-implementer-a", Share: 0.220 / 3, Mu: 14.872, Sigma: 1.924, Milestones: true},
	{Name: "node implementer B", Profile: ProfilePrefix + "node-implementer-b", Share: 0.220 / 3, Mu: 14.872, Sigma: 1.924, Milestones: true},
	{Name: "node implementer C", Profile: ProfilePrefix + "node-implementer-c", Share: 0.220 / 3, Mu: 14.872, Sigma: 1.924, Milestones: true},
	{Name: "tooling developer", Profile: ProfilePrefix + "tooling-developer", Share: 0.288, Mu: 13.630, Sigma: 1.376, Milestones: true},
	{Name: "infrastructure", Profile: ProfilePrefix + "infrastructure", Share: 0.169, Mu: 15.755, Sigma: 1.332, Milestones: true},
	{Name: "education/devrel", Profile: ProfilePrefix + "education-devrel", Share: 0.093, Mu: 15.206, Sigma: 0.750, Milestones: true},
	{Name: "governance administrator", Profile: ProfilePrefix + "governance-administrator", Share: 0.085, Mu: 14.300, Sigma: 1.961},
	{Name: "community fund", Profile: ProfilePrefix + "community-fund", Share: 0.076, Mu: 15.677, Sigma: 1.762},
	{Name: "research lab", Profile: ProfilePrefix + "research-lab", Share: 0.051, Mu: 14.919, Sigma: 2.293, Milestones: true},
	{Name: "security auditor", Profile: ProfilePrefix + "security-auditor", Share: 0.017, Mu: 16.334, Sigma: 0.5, Milestones: true},
}

// Administrator is the profile of the recipient that oversees milestone
// draws.
const Administrator = ProfilePrefix + "governance-administrator"

// Kinds returns the recipient types.
func Kinds() []Kind { return slices.Clone(kinds) }

// ByProfile returns the recipient type whose profile is name.
func ByProfile(name string) (Kind, bool) {
	i := slices.IndexFunc(kinds, func(k Kind) bool { return k.Profile == name })
	if i < 0 {
		return Kind{}, false
	}
	return kinds[i], true
}

// Ask is one payee of a withdrawal action.
type Ask struct {
	// Kind is the recipient's profile name.
	Kind string
	// Lovelace is what the action pays it.
	Lovelace uint64
}

// Draw is one withdrawal action's payees, drawn from r for a treasury
// holding treasury lovelace, among the recipients eligible reports
// (registered to receive). Each payee's type is drawn by share, without
// repeats, and its amount from its type's fit scaled by treasury over
// MainnetTreasury; the action's total is held to maxShare of the
// treasury by scaling every ask down together. None eligible draws none.
func Draw(r *rand.Rand, treasury uint64, eligible func(profile string) bool) []Ask {
	var pool []Kind
	for _, k := range kinds {
		if eligible(k.Profile) {
			pool = append(pool, k)
		}
	}

	n := 1
	if r.Float64() < bundleShare {
		n = 2 + r.IntN(maxBundle-1)
	}

	scale := float64(treasury) / MainnetTreasury
	var out []Ask
	var total float64
	for range min(n, len(pool)) {
		i := pick(r, pool)
		k := pool[i]
		pool = slices.Delete(pool, i, i+1)
		ada := math.Exp(k.Mu + k.Sigma*r.NormFloat64())
		out = append(out, Ask{Kind: k.Profile, Lovelace: uint64(ada * 1e6 * scale)})
		total += ada * 1e6 * scale
	}
	return capped(out, total, maxShare*float64(treasury))
}

// pick draws an index of pool by share.
func pick(r *rand.Rand, pool []Kind) int {
	var sum float64
	for _, k := range pool {
		sum += k.Share
	}

	u := r.Float64() * sum
	for i, k := range pool {
		if u < k.Share {
			return i
		}
		u -= k.Share
	}
	return len(pool) - 1
}

// capped scales asks down together so their total is at most limit, and
// drops any that round to nothing.
func capped(asks []Ask, total, limit float64) []Ask {
	f := 1.0
	if total > limit {
		f = limit / total
	}

	out := asks[:0]
	for _, a := range asks {
		if a.Lovelace = uint64(float64(a.Lovelace) * f); a.Lovelace > 0 {
			out = append(out, a)
		}
	}
	return out
}
