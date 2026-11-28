// Package household is a human's monthly money: pay, bills, employment and
// the share of what is left that goes into ADA. It is pure: each function
// takes a human's budget and state and the stream to draw from, and returns
// what changes; the cohort applies the result and the engine turns a
// purchase or a forced sale into an exchange order.
//
// Every amount is in integer cents (1/100 of the human's currency, for
// every currency, so JPY and KRW stay exact and every total adds up). The
// distributions and rates come from the profile's humans section and are
// synthetic, not fitted.
//
// One money step runs every Every months of a human's life:
//
//	step k: k % Steps == 0 ? Month (employment, pay, bills) : -
//	        Tranche (a purchase of ADA, when fiat allows)
//
// so a month has Steps purchases and one payday.
package household
