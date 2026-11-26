// Package chain is the engine's knowledge of the chain: a pure View folded
// from blocks, and the shells that read a node (Follower over chain-sync,
// Querier over local-state-query).
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
// stake-floor guard's input (policy.go). The Querier asks the node for what
// blocks cannot give (lsq.go lists what gouroboros lacks); the fold stays
// the source of per-wallet schedules.
//
// The Follower does not touch the View. It sends each chain-sync message
// to the engine, which logs it before applying it (models/specs/
// engine-recovery, design A). After every (re)connect the first event is
// Intersected: the FindIntersect answer, which the engine treats as a
// rollback because the node may have rolled back while it was away.
//
//	```mermaid
//	flowchart LR
//	  node[(cardano-node)] -- chain-sync --> src[Source: Node / fake]
//	  src --> fol[Follower]
//	  fol -- Event --> eng[engine loop]
//	  eng -- log first --> log[(event log)]
//	  eng -- Apply / Rollback --> view[View]
//	  node -- local-state-query --> q[Querier]
//	  q -- params, epoch, returns --> eng
//	```
package chain
