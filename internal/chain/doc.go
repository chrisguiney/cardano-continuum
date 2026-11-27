// Package chain is the engine's knowledge of the chain: a pure View folded
// from blocks, and the values the node client (package chain/node) delivers:
// Event, Point, Block, Genesis.
//
// The View tracks only what a Book names: the engine's addresses, stake
// credentials, pools and DReps. It keeps undo records for the blocks above
// its settled point (the newest block with K successors), so rollbacks
// within K are exact and a deeper one is ErrTooDeep. The settled point never
// moves back. A restart resumes from Base, the state at the settled point,
// with chain-sync intersecting at that point, as
// models/specs/planner-reservations requires; rebuilding from the origin
// would let the planner offer outputs it already settled as spent.
//
// Beyond UTxOs the View folds the delegation, pool, DRep, proposal and vote
// state from certificates and the governance fields of each block, and the
// epoch transition: the first block of an epoch rotates the mark and set
// stake snapshots and applies pool retirements (ledger.go). It answers the
// agendas' Governance and pool stake questions, and PolicyState builds the
// stake-floor guard's input (policy.go). The node's local-state-query
// answers what blocks cannot give; the fold stays the source of per-wallet
// schedules.
//
//	```mermaid
//	flowchart LR
//	  node[(cardano-node)] -- chain-sync --> fol[node.Follower]
//	  fol -- Event --> eng[engine loop]
//	  eng -- log first --> log[(event log)]
//	  eng -- Apply / Rollback --> view[View]
//	  node -- local-state-query --> q[node.Querier]
//	  q -- params, epoch, returns --> eng
//	```
package chain
