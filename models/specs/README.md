# models/specs

One directory per TLA+ model, each with its `.tla` modules and `.cfg` files;
`make models` runs `scripts/run-model.sh` on each.

| Directory | Models |
|---|---|
| `hello` | a toy keep-alive protocol for learning the tools |
| [`dex-contention`](dex-contention/README.md) | trades racing for the one market output |
| [`engine-recovery`](engine-recovery/README.md) | engine crash, restart and rollback recovery |
| [`fiat-escrow`](fiat-escrow/README.md) | a human's fiat held by its open buy orders |
| [`governance-change`](governance-change/README.md) | the proposer's parameter-change chain, the votes on it and hard forks |
| [`planner-reservations`](planner-reservations/README.md) | which outputs the planner may spend (reservations) |
| [`pool-forging`](pool-forging/README.md) | who may forge for a pool, and with which opcert |
| [`stake-floor`](stake-floor/README.md) | the engine's go-dark, retire and delegation guard |
| [`submit-retry`](submit-retry/README.md) | an actor's submission and retry of its transaction |
