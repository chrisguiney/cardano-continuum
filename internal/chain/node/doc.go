// Package node is the engine's client of a cardano-node: the Follower over
// chain-sync and the Querier over local-state-query. It does I/O only; the
// pure View it feeds is package chain.
//
// The Follower does not touch the View. It sends each chain-sync message
// to the engine, which logs it before applying it (models/specs/
// engine-recovery, design A). After every (re)connect the first event is
// Intersected: the FindIntersect answer, which the engine treats as a
// rollback because the node may have rolled back while it was away.
//
// The Querier asks the node for what blocks cannot give (lsq.go lists what
// gouroboros lacks).
//
//	```mermaid
//	flowchart LR
//	  node[(cardano-node)] -- chain-sync --> src[Source: Node / fake]
//	  src --> fol[Follower]
//	  fol -- chain.Event --> eng[engine loop]
//	  node -- local-state-query --> q[Querier]
//	  q -- params, epoch, returns --> eng
//	```
package node
