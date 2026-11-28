package profile

import (
	"fmt"
	"math"
	"slices"

	"gopkg.in/yaml.v3"
)

// Humans parameterises the people who own the wallets. A wallet profile
// still supplies a wallet's mechanics (its fitted clocks, amounts, budget
// and hazards); a human is the person behind one or more wallets: the
// owner's motivation and temperament, its wealth tier, how many wallets it
// keeps, and its household money (income, expenses, savings in fiat and the
// share it invests in ADA).
//
// Every number here is synthetic: chosen to be plausible and documented as
// such, never fitted from chain data. A file without a humans section runs
// each wallet as its own owner; a section that leaves a field out takes
// the DefaultHumans value.
type Humans struct {
	// Month is the simulated seconds in a month: the period of income,
	// expenses and employment changes. 300 compresses a month into five
	// minutes so a fake run sees several paydays; a realistic run sets
	// 2592000.
	Month float64 `yaml:"month"`
	// MaxWallets caps the wallets one human owns.
	MaxWallets int `yaml:"max_wallets"`
	// Spread is the owner trait in [0, 1] that spreads its wallets'
	// delegations over different pools and DReps.
	Spread Distr `yaml:"spread"`
	// Tiers are the wealth tiers humans are drawn into, by share.
	Tiers []Tier `yaml:"tiers"`
	// Invest is the investment strategy every human draws from.
	Invest Invest `yaml:"invest"`
}

// Tier is one wealth tier. Amounts are in US dollars per month (income) or
// once (fiat); each human converts them into its own currency at birth.
type Tier struct {
	// Name names the tier.
	Name string `yaml:"name"`
	// Share is the share of humans in the tier.
	Share float64 `yaml:"share"`
	// Wallets is the mean of the extra wallets (Poisson) a human keeps on
	// top of its first.
	Wallets float64 `yaml:"wallets"`
	// Income is the monthly pay while employed, in USD.
	Income Distr `yaml:"income"`
	// Expenses is the monthly obligations as a share of income.
	Expenses Distr `yaml:"expenses"`
	// Fiat is the savings in fiat a human starts with, in USD.
	Fiat Distr `yaml:"fiat"`
	// Layoff is the monthly chance an employed human loses its job.
	Layoff float64 `yaml:"layoff"`
	// Hire is the monthly chance an unemployed human finds one.
	Hire float64 `yaml:"hire"`
	// Benefit is the share of income an unemployed human still receives.
	Benefit float64 `yaml:"benefit"`
}

// Invest is the share of disposable income a human puts into ADA and how
// often.
type Invest struct {
	// Share is the fraction of disposable income (income less expenses)
	// invested, drawn per human.
	Share Distr `yaml:"share"`
	// Every is the months between purchases.
	Every float64 `yaml:"every"`
	// Buffer is the months of expenses kept in fiat before anything is
	// invested.
	Buffer float64 `yaml:"buffer"`
}

// Distr is a distribution written in a profile file as "name(args)".
type Distr struct{ Dist }

// UnmarshalYAML parses the distribution from its string form.
func (d *Distr) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}

	dist, err := parseDist(s)
	if err != nil {
		return fmt.Errorf("humans: line %d: %w", n.Line, err)
	}
	d.Dist = dist
	return nil
}

// MarshalYAML writes the distribution in its string form.
func (d Distr) MarshalYAML() (any, error) {
	if d.Dist == nil {
		return "", nil
	}
	return d.String(), nil
}

// IsZero reports an unset distribution, so yaml omits it.
func (d Distr) IsZero() bool { return d.Dist == nil }

// Humans defaults. The tiers are synthetic: a rough split of a retail
// population into low, mid, high and whale, not fitted to any data.
const (
	defaultMonth      = 300
	defaultMaxWallets = 12
	// maxWalletsCap bounds max_wallets; key derivation bounds actors, and
	// a person with more wallets than this is an institution.
	maxWalletsCap = 64
)

// DefaultHumans is the humans section a bare `humans: {}` gets.
func DefaultHumans() Humans {
	d := func(s string) Distr {
		v, err := parseDist(s)
		if err != nil {
			panic(err)
		}
		return Distr{v}
	}

	return Humans{
		Month:      defaultMonth,
		MaxWallets: defaultMaxWallets,
		Spread:     d("beta(2, 2)"),
		Tiers: []Tier{
			{Name: "low", Share: 0.45, Wallets: 0.2, Income: d("lognormal(7.3, 0.35)"), Expenses: d("beta(19, 1)"), Fiat: d("lognormal(5.5, 1)"), Layoff: 0.04, Hire: 0.25, Benefit: 0.3},
			{Name: "mid", Share: 0.35, Wallets: 0.6, Income: d("lognormal(8.2, 0.3)"), Expenses: d("beta(17, 3)"), Fiat: d("lognormal(7.5, 1)"), Layoff: 0.02, Hire: 0.3, Benefit: 0.3},
			{Name: "high", Share: 0.15, Wallets: 1.5, Income: d("lognormal(9.2, 0.3)"), Expenses: d("beta(7, 3)"), Fiat: d("lognormal(9.5, 1)"), Layoff: 0.01, Hire: 0.4, Benefit: 0.3},
			{Name: "whale", Share: 0.05, Wallets: 3, Income: d("lognormal(10.5, 0.5)"), Expenses: d("beta(3, 7)"), Fiat: d("lognormal(12, 1)"), Layoff: 0.005, Hire: 0.5, Benefit: 0.5},
		},
		Invest: Invest{Share: d("beta(2, 8)"), Every: 0.5, Buffer: 1},
	}
}

// fill gives every zero field of h its default. A tier takes the missing
// fields of the default tier with its name, or of the mid tier.
func (h Humans) fill() Humans {
	d := DefaultHumans()
	or := func(v *float64, def float64) {
		if *v == 0 {
			*v = def
		}
	}
	dist := func(v *Distr, def Distr) {
		if v.Dist == nil {
			*v = def
		}
	}

	or(&h.Month, d.Month)
	if h.MaxWallets == 0 {
		h.MaxWallets = d.MaxWallets
	}

	dist(&h.Spread, d.Spread)
	dist(&h.Invest.Share, d.Invest.Share)
	or(&h.Invest.Every, d.Invest.Every)
	or(&h.Invest.Buffer, d.Invest.Buffer)
	if len(h.Tiers) == 0 {
		h.Tiers = d.Tiers
	}

	h.Tiers = slices.Clone(h.Tiers)
	for i := range h.Tiers {
		h.Tiers[i] = h.Tiers[i].fill(d.Tiers)
	}
	return h
}

func (t Tier) fill(defaults []Tier) Tier {
	def := defaults[1]
	if i := slices.IndexFunc(defaults, func(x Tier) bool { return x.Name == t.Name }); i >= 0 {
		def = defaults[i]
	}

	for _, f := range []struct {
		v   *float64
		def float64
	}{{&t.Wallets, def.Wallets}, {&t.Layoff, def.Layoff}, {&t.Hire, def.Hire}, {&t.Benefit, def.Benefit}} {
		if *f.v == 0 {
			*f.v = f.def
		}
	}

	for _, f := range []struct {
		v   *Distr
		def Distr
	}{{&t.Income, def.Income}, {&t.Expenses, def.Expenses}, {&t.Fiat, def.Fiat}} {
		if f.v.Dist == nil {
			*f.v = f.def
		}
	}
	return t
}

// validate checks a filled section.
func (h Humans) validate() error {
	if !(h.Month > 0) || math.IsInf(h.Month, 0) {
		return fieldErr("month", "must be > 0 seconds, got %s", ff(h.Month))
	}

	if h.MaxWallets < 1 || h.MaxWallets > maxWalletsCap {
		return fieldErr("max_wallets", "must be in [1, %d], got %d", maxWalletsCap, h.MaxWallets)
	}

	if !(h.Invest.Every > 0) || math.IsInf(h.Invest.Every, 0) {
		return fieldErr("invest.every", "must be > 0 months, got %s", ff(h.Invest.Every))
	}

	if err := finiteNonNeg("invest.buffer", h.Invest.Buffer); err != nil {
		return err
	}
	return validateTiers(h.Tiers)
}

func validateTiers(ts []Tier) error {
	seen := map[string]bool{}
	var sum float64

	for i, t := range ts {
		field := fmt.Sprintf("tiers[%d]", i)
		if !identRE.MatchString(t.Name) || seen[t.Name] {
			return fieldErr(field+".name", "%q must be distinct and match %s", t.Name, identRE)
		}

		seen[t.Name] = true
		for _, f := range []struct {
			name string
			v    float64
		}{{"share", t.Share}, {"layoff", t.Layoff}, {"hire", t.Hire}, {"benefit", t.Benefit}} {
			if err := share(field+"."+f.name, f.v); err != nil {
				return err
			}
		}

		if err := finiteNonNeg(field+".wallets", t.Wallets); err != nil {
			return err
		}
		sum += t.Share
	}

	if math.Abs(sum-1) > weightTolerance {
		return fieldErr("tiers", "shares must sum to 1, got %s", ff(sum))
	}
	return nil
}

// parseHumans fills and validates a file's humans section; nil stays nil.
func parseHumans(raw *Humans) (*Humans, error) {
	if raw == nil {
		return nil, nil
	}

	h := raw.fill()
	if err := h.validate(); err != nil {
		return nil, fmt.Errorf("humans: %w", err)
	}
	return &h, nil
}
