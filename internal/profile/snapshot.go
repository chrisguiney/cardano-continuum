package profile

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed fxsnapshot.json
var snapshotJSON []byte

// Snapshot is the committed record of live FX rates and the ADA price that
// seeds DefaultMarkets. `make market-snapshot` regenerates it from public
// APIs; nothing at run time fetches, so a run depends on the file alone.
type Snapshot struct {
	// Fetched is the date the rates were fetched, YYYY-MM-DD.
	Fetched string `json:"fetched"`
	// Sources names where the rates came from.
	Sources SnapshotSources `json:"sources"`
	// ADA is the ADA/USD price and its fitted yearly dynamics.
	ADA SnapshotADA `json:"ada"`
	// Currencies are the fitted currencies by ISO 4217 code.
	Currencies map[string]SnapshotCurrency `json:"currencies"`
}

// SnapshotSources names the snapshot's data sources.
type SnapshotSources struct {
	// FX is the foreign exchange source.
	FX string `json:"fx"`
	// ADA is the ADA price source.
	ADA string `json:"ada"`
	// ViaADA lists the currencies whose USD rate is ADA in that currency
	// over ADA in USD, because FX does not publish them.
	ViaADA []string `json:"via_ada"`
}

// SnapshotADA is ADA/USD at the snapshot, with its fitted yearly log vol
// and drift (informational: the ADA path keeps its regime parameters).
type SnapshotADA struct {
	// USD is the price now.
	USD float64 `json:"usd"`
	// Vol is the annualised volatility of daily log returns.
	Vol float64 `json:"vol"`
	// Drift is the log change per year.
	Drift float64 `json:"drift"`
}

// SnapshotCurrency is one currency's rate and fitted dynamics against USD.
// One simulated hour stands for one year here.
type SnapshotCurrency struct {
	// USD is the USD one unit buys.
	USD float64 `json:"usd"`
	// Vol is the annualised volatility of daily log returns.
	Vol float64 `json:"vol"`
	// Drift is the log change per year; negative when the currency loses
	// value against USD.
	Drift float64 `json:"drift"`
	// Reversion is the Ornstein-Uhlenbeck pull per year around the trend.
	Reversion float64 `json:"reversion"`
}

// LoadSnapshot returns the embedded snapshot. The file is part of the
// build, so a malformed one is a programming error and panics; a test
// checks it.
func LoadSnapshot() Snapshot {
	var s Snapshot
	if err := json.Unmarshal(snapshotJSON, &s); err != nil {
		panic(fmt.Sprintf("profile: embedded fxsnapshot.json: %v", err))
	}
	return s
}

// SnapshotDate is the date the embedded snapshot was fetched.
func SnapshotDate() string { return LoadSnapshot().Fetched }
