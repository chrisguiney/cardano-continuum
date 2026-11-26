// Package phase judges a ContinuumPool's phase from its pod and its chain.
// PhaseOf reads the pod alone; Watch follows each Ready pod over chain-sync
// and Judge, which is pure, refines Syncing to Following or Forging from the
// tip, the newest block the pool's own cold key issued (IssuerOf) and the
// network's clock and leader schedule (Facts, read by FactsOf from the
// genesis).
package phase
