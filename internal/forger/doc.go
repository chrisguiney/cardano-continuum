// Package forger is continuum-forger: a stake pool that breaks consensus
// rules on purpose, to see how the honest nodes of a devnet react.
//
// A Node follows one upstream node's chain over node-to-node chain-sync and
// block-fetch, computes its own pool's leadership from the Praos nonce it
// evolves along that chain, forges Conway blocks with the pool's keys, and
// serves chain-sync and block-fetch to every node that connects. With an
// empty Schedule it is an honest pool, and stands in for one. A Schedule
// enables misbehaviours; each breaks one rule and nothing else:
//
//	equivocate        two valid blocks for one slot, to disjoint peers
//	opcert-rewind     certificate counter below the pool's on chain
//	opcert-leap       certificate counter more than one above it
//	kes-before-start  certificate whose KES period has not begun
//	kes-expired       certificate past its last KES evolution
//	kes-wrong-period  header signed at another KES evolution
//	vrf-bad-proof     leader proof made for another slot
//	vrf-not-leader    block in a slot the pool does not lead
//	body-hash         body the header does not commit to
//	oversize-body     body over the maximum block body size
//	future-slot       block for a slot that has not started
//
// Every decision is drawn from (seed, misbehaviour, slot), so a seed
// reproduces a run, and every forged block is logged as a "forged" record
// with what an honest node must do with it: adopt, or reject and name the
// rule. A block expecting reject that turns up on an honest node's chain is
// oracle.RogueAdopted.
//
// The forger holds a pool's hot keys only. A misbehaviour that needs
// another operational certificate (a lower counter, a later start) uses one
// the engine issued into the scenario's keys Secret beside the usual three
// files; the fence around who may forge for a pool is
// models/specs/pool-forging.
//
// Planning (Planner), block assembly (Assemble), the nonce (Nonces) and the
// chain and peer views (Chain) are pure. Node is the I/O around them.
//
//	```mermaid
//	flowchart LR
//	  up[upstream node] -- chain-sync, block-fetch --> follow
//	  follow --> chain[(Chain: followed, own, offers)]
//	  clock[slot clock] --> plan[Planner.Plan]
//	  chain -- tip, nonce, counter --> plan
//	  plan -- Forge --> asm[Assemble]
//	  asm -- Block --> chain
//	  asm -- forged record, CBOR --> log[(log, evidence dir)]
//	  chain -- per-peer view --> serve[chain-sync and block-fetch servers]
//	  serve --> peers[honest nodes]
//	```
//
// Limits: the chain must be in Conway from slot 0 (internal/genesis renders
// it so); the pool's stake share is the genesis delegation and protocol
// parameters are the genesis ones, neither followed from the ledger; blocks
// carry no transactions.
package forger
