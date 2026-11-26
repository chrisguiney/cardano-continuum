// Package pool reconciles ContinuumPools into workloads.
//
// Pools is the controller. It reads through a planner (what workload the
// pool gets, or why none), writes through an applier (the StatefulSet,
// Service and ConfigMap) and tells running nodes to reread their Config
// through a reloader. The phase it reports comes from package phase.
package pool
