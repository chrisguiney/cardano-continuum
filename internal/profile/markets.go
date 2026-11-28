package profile

import (
	"fmt"
	"math"
	"regexp"
	"slices"
)

// Markets parameterises the ADA market the population trades in
// (internal/market): the currencies wallets think in, the exchanges they
// trade through, and how the ADA rate moves births. Times are seconds of
// simulated time and rates are per hour of it: a fake run lasts minutes,
// so the defaults compress a market's swings into that span on purpose.
//
// A file without a markets section has no market; a section that leaves a
// field out takes the field's DefaultMarkets value.
type Markets struct {
	// Step is the seconds between moves of every price path; the paths are
	// constant in between.
	Step float64 `yaml:"step"`
	// QuoteEvery is the seconds between Quote records in the log.
	QuoteEvery float64 `yaml:"quote_every"`
	// ADA is the ADA/USD path.
	ADA ADAPath `yaml:"ada"`
	// FX is the currencies' paths against USD.
	FX FXPath `yaml:"fx"`
	// Currencies are the currencies wallets hold, by population share.
	Currencies []Currency `yaml:"currencies"`
	// Exchanges are the venues that quote ADA.
	Exchanges []Exchange `yaml:"exchanges"`
	// Extra is the chance of each further preferred currency: a wallet has
	// k extra currencies with probability (1-Extra) Extra^k.
	Extra float64 `yaml:"extra"`
	// Venue is how a wallet picks an exchange.
	Venue Venue `yaml:"venue"`
	// Births is how the ADA rate in a currency moves that currency's births.
	Births BirthResponse `yaml:"births"`
}

// ADAPath is a regime-switching log random walk with jumps: per step of dt
// hours, log rate moves by (Drift*regime - Vol²/2) dt + Vol √dt z, regime
// is +1 (bull) or -1 (bear) and flips at RegimeRate per hour, and jumps of
// log size N(JumpMean, JumpSD) arrive at JumpRate per hour.
type ADAPath struct {
	// Start is ADA/USD at time zero.
	Start float64 `yaml:"start"`
	// Vol is the volatility per √hour.
	Vol float64 `yaml:"vol"`
	// Drift is the regime's drift per hour, in log units.
	Drift float64 `yaml:"drift"`
	// RegimeRate is regime flips per hour.
	RegimeRate float64 `yaml:"regime_rate"`
	// JumpRate is jumps per hour.
	JumpRate float64 `yaml:"jump_rate"`
	// JumpMean and JumpSD are a jump's log size.
	JumpMean float64 `yaml:"jump_mean"`
	JumpSD   float64 `yaml:"jump_sd"`
}

// FXPath moves each currency's USD rate as a log Ornstein-Uhlenbeck
// process around its Currency.USD level.
type FXPath struct {
	// Vol is the volatility per √hour.
	Vol float64 `yaml:"vol"`
	// Reversion is the pull back to the level per hour.
	Reversion float64 `yaml:"reversion"`
}

// Currency is one fiat currency.
type Currency struct {
	// Code is the ISO 4217 code.
	Code string `yaml:"code"`
	// Share is the share of wallets whose primary currency it is.
	Share float64 `yaml:"share"`
	// USD is the USD one unit buys at time zero.
	USD float64 `yaml:"usd"`
}

// Exchange is one venue. Its quotes are the mid rate times its basis, a
// log Ornstein-Uhlenbeck drift of its own, less or plus half its spread.
type Exchange struct {
	// Name names the exchange and its actor's profile (exchange-<name>).
	Name string `yaml:"name"`
	// Spread is ask over bid, minus one.
	Spread float64 `yaml:"spread"`
	// Fee is the lovelace a withdrawal pays on chain beside the fee.
	Fee uint64 `yaml:"fee"`
	// Float is the lovelace the exchange's hot wallet holds: its budget,
	// which the genesis or the faucet tops up.
	Float uint64 `yaml:"float"`
	// Currencies are the codes the exchange quotes in.
	Currencies []string `yaml:"currencies"`
	// BasisVol and BasisReversion shape the basis, per √hour and per hour.
	BasisVol       float64 `yaml:"basis_vol"`
	BasisReversion float64 `yaml:"basis_reversion"`
}

// Venue is how a wallet picks among the exchanges that quote one of its
// currencies: the lowest effective price per ADA in its primary currency,
// after Conversion for a non-primary one, Loyalty off its last venue, and
// Gumbel noise of scale Noise.
type Venue struct {
	Conversion float64 `yaml:"conversion"`
	Loyalty    float64 `yaml:"loyalty"`
	Noise      float64 `yaml:"noise"`
}

// BirthResponse scales a currency's birth hazard by
// clamp(exp(Beta * ln(rate now / rate Window seconds ago)), Min, Max).
type BirthResponse struct {
	Beta   float64 `yaml:"beta"`
	Window float64 `yaml:"window"`
	Min    float64 `yaml:"min"`
	Max    float64 `yaml:"max"`
}

const ada = 1_000_000

// DefaultMarkets is the market a bare markets section gets.
func DefaultMarkets() Markets {
	return Markets{
		Step: 10, QuoteEvery: 60,
		ADA: ADAPath{Start: 0.6, Vol: 0.5, Drift: 0.4, RegimeRate: 2, JumpRate: 1, JumpMean: -0.02, JumpSD: 0.08},
		FX:  FXPath{Vol: 0.02, Reversion: 1},
		Currencies: []Currency{
			{Code: "USD", Share: 0.35, USD: 1},
			{Code: "EUR", Share: 0.2, USD: 1.08},
			{Code: "JPY", Share: 0.15, USD: 0.0067},
			{Code: "KRW", Share: 0.1, USD: 0.00074},
			{Code: "GBP", Share: 0.1, USD: 1.27},
			{Code: "BRL", Share: 0.1, USD: 0.2},
		},
		Exchanges: []Exchange{
			{Name: "north", Spread: 0.004, Fee: ada / 2, Float: 50_000 * ada, Currencies: []string{"USD", "EUR", "GBP"}, BasisVol: 0.003, BasisReversion: 4},
			{Name: "harbor", Spread: 0.006, Fee: 3 * ada / 10, Float: 50_000 * ada, Currencies: []string{"USD", "JPY", "KRW"}, BasisVol: 0.003, BasisReversion: 4},
			{Name: "summit", Spread: 0.01, Fee: ada / 5, Float: 50_000 * ada, Currencies: []string{"EUR", "BRL", "GBP", "USD"}, BasisVol: 0.003, BasisReversion: 4},
		},
		Extra:  0.15,
		Venue:  Venue{Conversion: 0.01, Loyalty: 0.003, Noise: 0.002},
		Births: BirthResponse{Beta: 4, Window: 300, Min: 0.25, Max: 4},
	}
}

var codeRE = regexp.MustCompile(`^[A-Z]{3}$`)

// fill gives every zero field of m its default. A list replaces the
// default list whole; an exchange's zero basis takes the default basis.
func (m Markets) fill() Markets {
	d := DefaultMarkets()
	or := func(v *float64, def float64) {
		if *v == 0 {
			*v = def
		}
	}
	or(&m.Step, d.Step)
	or(&m.QuoteEvery, d.QuoteEvery)
	or(&m.ADA.Start, d.ADA.Start)
	or(&m.ADA.Vol, d.ADA.Vol)
	or(&m.ADA.Drift, d.ADA.Drift)
	or(&m.ADA.RegimeRate, d.ADA.RegimeRate)
	or(&m.ADA.JumpRate, d.ADA.JumpRate)
	or(&m.ADA.JumpMean, d.ADA.JumpMean)
	or(&m.ADA.JumpSD, d.ADA.JumpSD)
	or(&m.FX.Vol, d.FX.Vol)
	or(&m.FX.Reversion, d.FX.Reversion)
	or(&m.Extra, d.Extra)
	or(&m.Venue.Conversion, d.Venue.Conversion)
	or(&m.Venue.Loyalty, d.Venue.Loyalty)
	or(&m.Venue.Noise, d.Venue.Noise)
	or(&m.Births.Beta, d.Births.Beta)
	or(&m.Births.Window, d.Births.Window)
	or(&m.Births.Min, d.Births.Min)
	or(&m.Births.Max, d.Births.Max)
	if len(m.Currencies) == 0 {
		m.Currencies = d.Currencies
	}
	if len(m.Exchanges) == 0 {
		m.Exchanges = d.Exchanges
	}
	m.Exchanges = slices.Clone(m.Exchanges)
	for i := range m.Exchanges {
		or(&m.Exchanges[i].BasisVol, d.Exchanges[0].BasisVol)
		or(&m.Exchanges[i].BasisReversion, d.Exchanges[0].BasisReversion)
	}
	return m
}

// validate checks a filled section.
func (m Markets) validate() error {
	for _, f := range []struct {
		name string
		v    float64
	}{
		{"step", m.Step}, {"quote_every", m.QuoteEvery}, {"ada.start", m.ADA.Start}, {"ada.vol", m.ADA.Vol},
		{"ada.regime_rate", m.ADA.RegimeRate}, {"ada.jump_rate", m.ADA.JumpRate}, {"ada.jump_sd", m.ADA.JumpSD},
		{"fx.vol", m.FX.Vol}, {"fx.reversion", m.FX.Reversion}, {"venue.conversion", m.Venue.Conversion},
		{"venue.loyalty", m.Venue.Loyalty}, {"venue.noise", m.Venue.Noise}, {"births.window", m.Births.Window},
		{"births.min", m.Births.Min},
	} {
		if err := finiteNonNeg("markets."+f.name, f.v); err != nil {
			return err
		}
	}
	if m.Step <= 0 || m.QuoteEvery < m.Step {
		return fieldErr("markets.quote_every", "must be at least step (%s), got %s", ff(m.Step), ff(m.QuoteEvery))
	}
	if math.IsNaN(m.ADA.Drift) || math.IsNaN(m.ADA.JumpMean) || math.IsNaN(m.Births.Beta) {
		return fieldErr("markets", "drift, jump_mean and births.beta must be numbers")
	}
	if !(m.Extra >= 0 && m.Extra < 1) {
		return fieldErr("markets.extra", "must be in [0, 1), got %s", ff(m.Extra))
	}
	if !(m.Births.Min > 0 && m.Births.Min <= m.Births.Max) || math.IsInf(m.Births.Max, 0) {
		return fieldErr("markets.births", "need 0 < min <= max, got min %s max %s", ff(m.Births.Min), ff(m.Births.Max))
	}
	if err := validateCurrencies(m.Currencies); err != nil {
		return err
	}
	return validateExchanges(m.Exchanges, m.Currencies)
}

func validateCurrencies(cs []Currency) error {
	seen := map[string]bool{}
	var sum float64

	for i, c := range cs {
		field := fmt.Sprintf("markets.currencies[%d]", i)
		if !codeRE.MatchString(c.Code) || seen[c.Code] {
			return fieldErr(field+".code", "%q must be a distinct ISO 4217 code", c.Code)
		}
		seen[c.Code] = true
		if err := share(field+".share", c.Share); err != nil {
			return err
		}
		if !(c.USD > 0) || math.IsInf(c.USD, 0) {
			return fieldErr(field+".usd", "must be > 0, got %s", ff(c.USD))
		}
		sum += c.Share
	}

	if math.Abs(sum-1) > weightTolerance {
		return fieldErr("markets.currencies", "shares must sum to 1, got %s", ff(sum))
	}
	return nil
}

func validateExchanges(xs []Exchange, cs []Currency) error {
	known := map[string]bool{}
	for _, c := range cs {
		known[c.Code] = true
	}
	names := map[string]bool{}

	for i, x := range xs {
		field := fmt.Sprintf("markets.exchanges[%d]", i)
		if !identRE.MatchString(x.Name) || names[x.Name] {
			return fieldErr(field+".name", "%q must be distinct and match %s", x.Name, identRE)
		}
		names[x.Name] = true
		if !(x.Spread >= 0 && x.Spread < 1) {
			return fieldErr(field+".spread", "must be in [0, 1), got %s", ff(x.Spread))
		}
		if x.Float == 0 {
			return fieldErr(field+".float", "must be > 0")
		}
		if err := finiteNonNeg(field+".basis_vol", x.BasisVol); err != nil {
			return err
		}
		if err := finiteNonNeg(field+".basis_reversion", x.BasisReversion); err != nil {
			return err
		}
		if len(x.Currencies) == 0 {
			return fieldErr(field+".currencies", "at least one is required")
		}
		for _, c := range x.Currencies {
			if !known[c] {
				return fieldErr(field+".currencies", "%q is not a markets currency", c)
			}
		}
	}
	return nil
}

// parseMarkets fills and validates a file's markets section; nil stays nil.
func parseMarkets(raw *Markets) (*Markets, error) {
	if raw == nil {
		return nil, nil
	}
	m := raw.fill()
	if err := m.validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
