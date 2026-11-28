// Package grants is the treasury's recipients and what they ask for:
// generic recipient types (never named organisations), each with its
// share of mainnet's treasury-withdrawal payees and a lognormal fit of the
// amounts they asked, and the draw of one withdrawal action's payees,
// scaled from mainnet's treasury to the run's.
//
// The fit is of mainnet's 111 treasury-withdrawal proposals as Koios
// served them on 2026-11-27 (api.koios.rest proposal_list, proposal_type
// TreasuryWithdrawals: 118 payees of at least 1000 ADA, 70 proposals
// enacted). Payees were typed from their proposals' CIP-108 titles by
// keyword. The mainnet treasury it is scaled from is Koios' totals for
// epoch 670.
//
// The package is pure: it draws only from the stream it is given.
package grants
