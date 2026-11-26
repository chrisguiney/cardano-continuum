// Package explorer indexes the chain the engine follows so the web UI can
// browse it: blocks, transactions, addresses and epochs.
//
// An Index is fed full Conway blocks in chain order and rolled back on
// rollbacks. It decodes with gouroboros' ledger types and keeps two tiers of
// state, so memory stays bounded however long the chain grows:
//
//   - Compact entries for every block, transaction and address the index has
//     seen: heights, hashes, issuer, counts, fees, ExUnits and each address's
//     transaction ids. These stay for the life of the index.
//   - Full blocks (the CBOR and the outputs its transactions spent) only for
//     the newest Config.Keep blocks. A transaction or block older than that
//     answers from its compact entry and says it is evicted.
//
// Rollback is exact within the retained blocks: the spent outputs a full block
// carries are put back. A rollback deeper than the retained window is
// ErrTooDeep, as chain.View treats a rollback below the settled point.
//
//	```mermaid
//	flowchart LR
//	  raw[block CBOR] --> add[Index.Add]
//	  add --> compact[(compact: meta, tx, address, epoch)]
//	  add --> full[(full: newest Keep blocks)]
//	  full --> views[Block / Tx / Address views]
//	  compact --> views
//	```
package explorer
