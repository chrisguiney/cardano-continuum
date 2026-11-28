// Package market is the ADA market a run's wallets trade in: an ADA/USD
// path, each currency's USD rate, and each exchange's quotes, as pure
// functions of the run seed and simulated time, with the draws a wallet
// makes against them (its currencies, its venue). The population's own
// trades do not move the paths: they are exogenous, so a birth stream can
// thin against them and a restore recomputes them instead of storing them.
//
// Every path moves once per profile.Markets.Step and is constant in
// between. Step k's draws come from seed.Derive("market", k), so the value
// at a time depends on nothing but the seed, the parameters and the time,
// whatever order the times are read in. A Path memoises checkpoints; it is
// not safe for concurrent use.
package market
