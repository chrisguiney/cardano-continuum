package household

import (
	"math"
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Style is how a human times its purchases.
type Style string

// Investment styles. DCA buys a fixed share; Dip buys more after a fall
// and less after a rise; Momentum the reverse.
const (
	DCA      Style = "dca"
	Dip      Style = "dip"
	Momentum Style = "momentum"
)

// Styles lists every investment style, sorted.
func Styles() []Style { return []Style{DCA, Dip, Momentum} }

// styles is the style each motivation invests with: holders and stakers
// average in, traders chase the trend, dapp users buy the dips.
var styles = map[profile.AgendaName]Style{
	"profit":      Momentum,
	"opportunist": Momentum,
	"dapp":        Dip,
}

// StyleOf is the style a human of the motivation invests with.
func StyleOf(motivation profile.AgendaName) Style {
	if s, ok := styles[motivation]; ok {
		return s
	}
	return DCA
}

// Synthetic constants of the monthly draws.
const (
	// noise is the spread of a month's pay and bills around their means:
	// each is scaled by a uniform draw in [1-noise/2, 1+noise/2].
	noise = 0.1
	// trendGain and the clamp shape Dip and Momentum: exp(±trendGain *
	// trend), within [minScale, maxScale].
	trendGain = 3
	minScale  = 0.25
	maxScale  = 4
	// centsPerUnit is the cents in a unit of any currency.
	centsPerUnit = 100
	lovelace     = 1_000_000
)

// Budget is a human's household, drawn once at its birth.
type Budget struct {
	// Income is the monthly pay while employed, in cents.
	Income int64
	// Ratio is the monthly bills as a share of Income.
	Ratio float64
	// Benefit is the share of Income paid while unemployed.
	Benefit float64
	// Layoff and Hire are the monthly chances of losing and finding a job.
	Layoff, Hire float64
	// Share is the fraction of disposable income invested in ADA.
	Share float64
	// Steps is the purchases a month: one money step each.
	Steps int
	// Buffer is the months of bills kept in fiat before investing.
	Buffer float64
	// Style times the purchases.
	Style Style
}

// State is what a human's money remembers between steps.
type State struct {
	// Employed reports a job.
	Employed bool
	// Disposable is last month's income less its bills, at least zero.
	Disposable int64
	// Bills is last month's bills due.
	Bills int64
}

// Draw draws a human's budget and starting fiat in cents from its tier and
// the investment strategy. usd is the US dollars one unit of the human's
// currency buys at its birth: the tier's amounts are in dollars, and the
// human's income is nominal in its own currency from then on. The draws
// come in a fixed order (income, bills, fiat, share).
func Draw(t profile.Tier, inv profile.Invest, motivation profile.AgendaName, usd float64, r *rand.Rand) (Budget, int64) {
	cents := func(dollars float64) int64 { return int64(math.Round(dollars / usd * centsPerUnit)) }
	income := cents(t.Income.Sample(r))
	ratio := min(max(t.Expenses.Sample(r), 0), 1)
	fiat := cents(t.Fiat.Sample(r))
	share := min(max(inv.Share.Sample(r), 0), 1)

	b := Budget{
		Income:  income,
		Ratio:   ratio,
		Benefit: t.Benefit,
		Layoff:  t.Layoff,
		Hire:    t.Hire,
		Share:   share,
		Steps:   max(1, int(math.Round(1/inv.Every))),
		Buffer:  inv.Buffer,
		Style:   StyleOf(motivation),
	}
	return b, max(fiat, 0)
}

// Monthly is what a month did to a human's money.
type Monthly struct {
	// Employed is the human's employment for the month, Switched whether
	// it changed.
	Employed, Switched bool
	// Income is the pay received.
	Income int64
	// Due, Paid and Short are the bills: what was owed, what the available
	// fiat paid and what it could not.
	Due, Paid, Short int64
	// Disposable is Income less Due, at least zero.
	Disposable int64
}

// Month runs a payday: the employment chain moves, the pay arrives, then
// the bills are paid from what is available (available plus the pay),
// never below zero. Draws: employment, pay noise, bill noise.
func Month(b Budget, s State, available int64, r *rand.Rand) Monthly {
	u := r.Float64()
	employed := u < b.Hire
	if s.Employed {
		employed = u >= b.Layoff
	}

	pay := float64(b.Income)
	if !employed {
		pay *= b.Benefit
	}

	income := int64(math.Round(pay * jitter(r)))
	due := int64(math.Round(float64(b.Income) * b.Ratio * jitter(r)))
	paid := min(due, max(available+income, 0))
	return Monthly{
		Employed:   employed,
		Switched:   employed != s.Employed,
		Income:     income,
		Due:        due,
		Paid:       paid,
		Short:      due - paid,
		Disposable: max(income-due, 0),
	}
}

func jitter(r *rand.Rand) float64 { return 1 + noise*(r.Float64()-0.5) }

// Tranche is the cents one step invests: the share of last month's
// disposable income over the month's steps, scaled by the style for the
// trend (the market's log return over its window), and never more than
// the available fiat above the buffer of bills. It draws nothing.
func Tranche(b Budget, s State, available int64, trend float64) int64 {
	scale := 1.0
	switch b.Style {
	case Dip:
		scale = min(max(math.Exp(-trendGain*trend), minScale), maxScale)
	case Momentum:
		scale = min(max(math.Exp(trendGain*trend), minScale), maxScale)
	}

	want := int64(b.Share * float64(s.Disposable) / float64(b.Steps) * scale)
	room := available - int64(math.Ceil(b.Buffer*float64(s.Bills)))
	return max(min(want, room), 0)
}

// Accrue adds a step's tranche to what the human has put aside (carry)
// and says how much to buy now: what is put aside, within the available
// fiat above the buffer, once it reaches least cents (the smallest trade
// worth making). The rest stays put aside, at most one month's investment,
// so a human short of its buffer for months does not build up a hoard.
func Accrue(b Budget, s State, carry, available, least int64, trend float64) (buy, left int64) {
	month := int64(b.Share * float64(s.Disposable))
	carry = min(carry+Tranche(b, s, math.MaxInt64, trend), max(month, 0))
	room := available - int64(math.Ceil(b.Buffer*float64(s.Bills)))
	buy = max(min(carry, room), 0)
	if buy < least || buy == 0 {
		return 0, carry
	}
	return buy, carry - buy
}

// Hold is the cents an order for gross lovelace at price (per ADA) holds:
// rounded up, so a settled buy never spends more than it held.
func Hold(gross uint64, price float64) int64 {
	return int64(math.Ceil(float64(gross) * price * centsPerUnit / lovelace))
}

// Proceeds is the cents a sale of lovelace at price brings: rounded down.
func Proceeds(amount uint64, price float64) int64 {
	return int64(math.Floor(float64(amount) * price * centsPerUnit / lovelace))
}

// Lovelace is what cents buy at price per ADA, rounded down.
func Lovelace(cents int64, price float64) uint64 {
	if price <= 0 || cents <= 0 {
		return 0
	}
	return uint64(math.Floor(float64(cents) / centsPerUnit / price * lovelace))
}
