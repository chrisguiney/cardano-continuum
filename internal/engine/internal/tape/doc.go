// Package tape folds a run's market records into what the summary line and
// the Markets page show: the Quote series, the Traded flows by exchange,
// currency and side, the births by currency, and the largest ADA/USD
// drawdown with how many wallets under water sold in it and how many held.
// It reads decision records only, so a replayed log folds the same.
package tape
