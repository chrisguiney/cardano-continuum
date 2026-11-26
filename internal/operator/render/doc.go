// Package render turns a ContinuumPool and its ContinuumNetwork into the
// StatefulSet, headless Service and ConfigMap of one node. Everything here is
// pure: Render, the NodeDriver implementations (CardanoNode, Dingo, Rogue),
// admission against the network budget (Admit), topology (Peers) and the
// naming and fence labels (Name, Role, Finalizer) that the other operator
// packages share.
//
// The fence contract the rendered names and finalizer implement is
// models/specs/pool-forging/README.md ("Operator fencing contract").
package render
