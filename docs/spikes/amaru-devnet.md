# Amaru on a blank devnet: phase-0 spike

Amaru `v10.11.20261119` (git `51437ea`), image `ghcr.io/pragma-org/amaru:v10.11.20261119`
(arm64). Source paths below are at that tag. `main` was checked on 2026-11-25 and has the same
blocking code. Commands ran in throwaway containers, with `$I` standing for the image and a
Conway-only era history file:

```json
{"stability_window":300,"eras":[{"start":{"time":0,"slot":0,"epoch":0},"end":null,
 "params":{"epoch_size_slots":500,"slot_length":1000,"era_name":"Conway"}}]}
```

**Outcome: C. Amaru cannot join a blank devnet.** It can only start from an imported ledger
state, and `amaru node bootstrap` refuses every network except mainnet, preprod and preview.

## Answers

1. **Does it accept `testnet_<magic>` with custom genesis and era history? Partly.** It parses the
   name, and `node run` takes `--era-history` (or `AMARU_ERA_HISTORY`).
   - Without the flag, `docker run $I node run --network testnet_42` logs
     `ERROR amaru::cli: error description="missing era history for custom network"`
     (`crates/amaru/src/bin/amaru/cmd/node/run.rs:633-639`).
   - With the flag, the file loads and startup carries on until it opens the database (see 2).
   - `node run` reads no genesis files. Genesis values come from global-parameter flags
     (`--system-start`, `--consensus-security-param`, `--epoch-length-scale-factor`,
     `--active-slot-coeff-inverse`, `--slots-per-kes-period`, `--max-kes-evolution`,
     `--max-lovelace-supply`). If they are left out, preprod's values are used without any
     warning: the log shows `system_start":1654041600000`.
   - There is no peer snapshot for a custom testnet (`PEER_SNAPSHOT_NETWORKS`,
     `crates/amaru-kernel/src/cardano/network_name.rs:37`), so `--peer` is required.
2. **Can it start from genesis, or import a snapshot from the devnet's cardano-node? No. This
   answer blocks questions 3-7.**
   - Starting from genesis fails. With empty ledger and chain directories, `node run --network
     testnet_42 --era-history ... --peer ...` logs `ERROR ... OpenError: Invalid argument:
     ./chain.testnet_42.db/CURRENT: does not exist (create_if_missing is false)`. Without the
     chain directory it logs `failed to stat db_chain`. The stores are only ever created by
     bootstrap, and bootstrap refuses an origin point (`crates/amaru-bootstrap/src/bootstrap/mod.rs:137-139`:
     "bootstrap snapshots must not use origin"). Maintainer statement: "It needs 3 epoch
     snapshots to bootstrap and cannot sync from genesis, even if the network immediately
     starts in Conway" (abailly, pragma-org/amaru issue 277 comment 3257328008).
   - Importing a snapshot fails too. I served a 404 index from a local HTTP server, then ran
     `node bootstrap --network testnet_42 --s3-public-url http://continuum-spike-www`. It logs
     `ERROR ... bootstrap operation failed: no era history available for network testnet_42;
     S3 bootstrap is only supported for mainnet, preprod, and preview`. The cause is
     `snapshots_with_epochs` (`bootstrap/mod.rs:176-179`): it calls
     `NetworkName::as_era_history()`, which returns `None` for `Testnet`
     (`network_name.rs:100-107`). `node bootstrap` has no `--era-history` flag
     (`cmd/node/bootstrap.rs:27-61`). Snapshots placed in the local snapshots directory go
     through the same function (`bootstrap/mod.rs:157-173`), so they don't get around it.
   - Making the snapshots looks possible, but I did not run it. `amaru snapshot create` has a
     custom-testnet path: `--cardano-node-config <DIR>`, `--cardano-node-db <DIR>` (local
     immutable chunks instead of Mithril) and three `--snapshot SLOT.HASH::PARENT` points
     (Koios bypass). It needs `db-analyser` on PATH (known working: 11.0.1), and the Amaru
     image does not ship it. It needs three consecutive epoch-end snapshots, so the devnet
     must be at least 4 epochs old: about 34 minutes at 500 one-second slots.
3. **Follows a 500-slot chain for 3 epochs?** Not reached: no ledger state.
4. **Accepts Conway at epoch 0? Source check only: yes.** `EraHistory::default()` is itself a
   single Conway era from slot 0 (`crates/amaru-kernel/src/cardano/era_history.rs:48-61`), and
   the one-era file above loaded. One constraint for the devnet genesis:
   `epoch_length = active_slot_coeff_inverse * epoch_length_scale_factor * k`
   (`crates/amaru-kernel/src/cardano/global_parameters.rs:125-127`). So `1/f` must be an
   integer and the epoch length must be a multiple of `k/f`. Example: k=5, f=0.05, scale=5
   gives 500 slots.
5. **Can it forge? Not tested.** Source check of the key formats: `--operator-kes` accepts raw
   bytes or a cardano-cli `kes.skey` text envelope, `--operator-vrf` a 32-byte seed or a
   `vrf.skey` envelope, and `--operator-cert` CBOR or a `node.cert` envelope. The cold key is
   read from the cert (`cmd/node/run.rs:101-136`). The shared keys Secret would work unchanged.
   Whether cardano-node adopts its blocks is unknown.
6. **NtC? Source check only: no.** The mini-protocols are N2N only: handshake, chainsync,
   blockfetch, keepalive, peer_sharing, tx_submission (`crates/amaru-protocols/src/lib.rs:21-40`).
   It has no local-state-query or local-tx-submission. Its only client-facing surface is an
   optional HTTP `POST /api/submit/tx` (`--submit-api-listen-on`). So Amaru could feed a fork
   monitor over N2N chainsync, but it could not be a differential-oracle source for ledger
   queries.
7. **Footprint and time to tip?** Not reached. The image is 54.7 MB. The binary exits within
   about 0.2 s on the errors above.

## Upstream gap to file

`node bootstrap` should accept `--era-history` for `testnet_<magic>`, the same way `node run`
does, and pass it to `snapshots_with_epochs` and `import_snapshot`. A genesis start for
Conway-at-0 networks (empty stores at origin) would remove the snapshot step altogether.

## What an Amaru driver would need (if the gap is fixed: outcome B, maybe A)

- A bootstrap Job that runs after cardano-node reaches epoch 4 or later. It runs
  `db-analyser --store-ledger` at three consecutive epoch-end slots against the cardano-node
  ImmutableDB (pin db-analyser to the node version), then `amaru snapshot create
  --network testnet_<magic> --cardano-node-config --cardano-node-db --epoch N` with three
  `--snapshot` points, then `amaru node bootstrap` from the local snapshots directory.
- Rendered config: an era-history JSON built from the genesis (Conway at 0, `stability_window`
  = 3k/f), the global-parameter env vars (`AMARU_GLOBAL_*`) copied from the Shelley genesis,
  and static `AMARU_PEER` addresses. Set `AMARU_TUI_OFF=true` in pods.
- Forging tier: mount the existing KES/VRF/opcert text envelopes as files.
- Oracle role: N2N chainsync only (fork monitor). Ledger answers are not available over NtC.

## Not checked

Questions 3, 5 (adoption), 6 (live check) and 7 were not run: they are blocked by question 2.
I built no cardano-node devnet, because Amaru fails before it opens a peer connection. I did
not patch Amaru to get past the bootstrap check, which would have meant a Rust build plus
db-analyser and a 4-epoch devnet, beyond the time box.
