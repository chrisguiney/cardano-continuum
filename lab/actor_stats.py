"""Events of the chain's non-wallet actors: pools, DReps, proposals, contracts.

The I/O half of fitting the non-wallet populations: DuckDB SQL over the
mithril2pq tables, returning plain rows in an ActorEvents. actor_fit.py
turns them into rates and distributions.

Pools and DReps exist from the moment they register until they retire, so
their head count at the end of the window depends on the whole history:
those tables (certificates, pool registrations, votes, proposals) are read
from the start of the chain. They are small (a mainnet has a few million
certificates). Contracts are read from the window's outputs only, plus one
pass over earlier outputs to tell a new contract from an old one.

Tables read (mithril2pq schema): tx, block, certificate,
certificate_credential, pool_registration, vote, proposal, tx_output.

README: "Non-wallet populations".
"""

from __future__ import annotations

from dataclasses import dataclass, field

import duckdb

from chain_stats import has_table, sql_text, table_glob

# A script address is one whose payment credential is a script: CIP-19
# header types 1, 3, 5 and 7. The header's high four bits are the type and
# the network id sits in the low four, which is 0 or 1, so the first bech32
# character after the separator is 2 * type: z, x, 2 or w.
# https://cips.cardano.org/cip/CIP-19
SCRIPT_ADDRESS_RE = "^addr(_test)?1[zx2w]"

# A contract is a script payment credential, not a full address: one dApp
# validator is paid at many addresses that differ only in the user's stake
# part. The credential is bytes 1..28 of the address, which bech32 spreads
# over characters 1..46 after the separator, not on character boundaries.
# Characters 1..45 hold 222 of its 224 bits (plus two network bits, fixed
# per network) and none of the stake part, so they name the credential:
# two scripts would have to share 222 bits of a hash to collide.
def contract_key(column: str) -> str:
    """SQL naming the contract of the address in column."""
    return f"substr({column}, strpos({column}, '1') + 2, 45)"


@dataclass
class ActorEvents:
    """Non-wallet events of one chain, up to the end of the fitted window.

    Every list is sorted, so a fit of the same data is the same to the bit.
    """

    # (pool id hex, slot, "register" | "retire", pledge, cost, margin,
    # retirement epoch): one row per pool certificate.
    pool_events: list[tuple] = field(default_factory=list)
    # (DRep credential hex, slot, "register" | "retire").
    drep_events: list[tuple] = field(default_factory=list)
    # (DRep credential hex, slot): one row per DRep vote.
    drep_votes: list[tuple] = field(default_factory=list)
    # (pool id hex, slot): one row per vote cast by a stake pool.
    pool_votes: list[tuple] = field(default_factory=list)
    # (action type, slot): one row per governance proposal.
    proposals: list[tuple] = field(default_factory=list)
    # Transactions paying each contract in the window, one count per contract.
    contract_use: list[int] = field(default_factory=list)
    # Contracts paid in the window that no earlier output paid.
    new_contracts: int = 0


def _read(root: str, table: str) -> str:
    return f"read_parquet({sql_text(table_glob(root, table))})"


def _event_slots(con: duckdb.DuckDBPyConnection, root: str, max_slot: int) -> None:
    """event_slot(tx_hash, slot, block_index): where every valid transaction
    that carries a certificate, a vote or a proposal sits, up to max_slot."""
    sources = [f"SELECT tx_hash FROM {_read(root, t)}" for t in ("certificate", "vote", "proposal")
               if has_table(root, t)]
    if not sources:
        con.execute("CREATE TEMP TABLE event_slot (tx_hash BLOB, slot UBIGINT, block_index UINTEGER)")
        return
    con.execute(f"""
CREATE TEMP TABLE event_tx AS {" UNION ".join(sources)};
CREATE TEMP TABLE event_slot AS
SELECT t.hash AS tx_hash, b.slot, t.block_index
FROM {_read(root, "tx")} t JOIN {_read(root, "block")} b ON t.block_hash = b.hash
WHERE t.valid AND b.slot <= {int(max_slot)} AND t.hash IN (SELECT tx_hash FROM event_tx);
""")


# Every pool registration (a first one or a parameter update) and every
# retirement certificate, in chain order: by slot, position in the block and
# position in the transaction. Replaying them depends on that order (a pool
# can re-register twice in one block), so it must be total.
POOL_EVENTS_SQL = """
SELECT pool, slot, what, pledge, cost, margin, retirement_epoch FROM (
  SELECT lower(hex(r.operator)) AS pool, e.slot, e.block_index, r.cert_index, 'register' AS what, r.pledge, r.cost,
         r.margin_numerator::DOUBLE / nullif(r.margin_denominator, 0) AS margin, NULL AS retirement_epoch
  FROM {pool_registration} r JOIN event_slot e USING (tx_hash)
  UNION ALL
  SELECT lower(hex(c.pool_key_hash)), e.slot, e.block_index, c.cert_index, 'retire', NULL, NULL, NULL,
         c.retirement_epoch
  FROM {certificate} c JOIN event_slot e USING (tx_hash)
  WHERE c.cert_type = 'pool_retirement')
ORDER BY slot, block_index, cert_index, pool, what
"""

DREP_EVENTS_SQL = """
SELECT lower(hex(k.hash)), e.slot,
       CASE c.cert_type WHEN 'drep_registration' THEN 'register' ELSE 'retire' END
FROM {certificate} c
JOIN {certificate_credential} k ON k.tx_hash = c.tx_hash AND k.cert_index = c.cert_index AND k.role = 'drep'
JOIN event_slot e ON e.tx_hash = c.tx_hash
WHERE c.cert_type IN ('drep_registration', 'drep_deregistration')
ORDER BY e.slot, e.block_index, c.cert_index, 1, 3
"""

DREP_VOTES_SQL = """
SELECT lower(hex(v.voter_hash)), e.slot FROM {vote} v JOIN event_slot e USING (tx_hash)
WHERE v.voter_role IN ('drep_key', 'drep_script') ORDER BY 2, 1
"""

POOL_VOTES_SQL = """
SELECT lower(hex(v.voter_hash)), e.slot FROM {vote} v JOIN event_slot e USING (tx_hash)
WHERE v.voter_role = 'stake_pool' ORDER BY 2, 1
"""

PROPOSALS_SQL = """
SELECT p.action_type, e.slot FROM {proposal} p JOIN event_slot e USING (tx_hash) ORDER BY 2, 1
"""

# Transactions paying each contract among the window's outputs
# (chain_stats' paid_output table).
CONTRACT_USE_SQL = f"""
CREATE OR REPLACE TEMP TABLE contract_use AS
SELECT {contract_key("address")} AS contract, count(DISTINCT hash) AS txs
FROM paid_output WHERE regexp_matches(address, '{SCRIPT_ADDRESS_RE}') GROUP BY 1
"""


def _rows(con: duckdb.DuckDBPyConnection, sql: str, root: str, tables: tuple[str, ...]) -> list[tuple]:
    """The rows of sql, or none when the chain lacks one of its tables."""
    if not all(has_table(root, t) for t in tables):
        return []
    return con.execute(sql.format(**{t: _read(root, t) for t in tables})).fetchall()


def _earlier_output_files(root: str, manifest: dict, first_chunk: int | None) -> tuple[str | None, str]:
    """SQL file lists of the tx_output files wholly before the window (None
    when there are none) and of those that may hold outputs from either
    side of its start. Without a manifest's chunk ranges every file is of
    the second kind."""
    entries = manifest.get("tables", {}).get("tx_output", [])
    if first_chunk is None or not entries:
        return None, sql_text(table_glob(root, "tx_output"))

    def files(chosen: list[dict]) -> str:
        return "[" + ", ".join(sql_text(f"{root}/tx_output/{e['name']}") for e in chosen) + "]"

    before = [e for e in entries if e["last_chunk"] < first_chunk]
    straddle = [e for e in entries if e["first_chunk"] <= first_chunk <= e["last_chunk"]]
    return (files(before) if before else None), files(straddle)


def count_new_contracts(con: duckdb.DuckDBPyConnection, root: str, first_slot: int, manifest: dict) -> int:
    """Contracts in contract_use that no output before first_slot paid.

    Files wholly before the window need no slot: any match there is older.
    Files straddling its start are joined to their blocks to keep only the
    outputs before it. The manifest's chunk ranges tell the two apart;
    without one every file is joined, which is slower but the same answer.
    """
    rows = con.execute(f"SELECT min(chunk) FROM {_read(root, 'block')} WHERE slot >= {int(first_slot)}").fetchall()
    before, straddle = _earlier_output_files(root, manifest, rows[0][0])
    key = contract_key("o.address")
    match = f"regexp_matches(o.address, '{SCRIPT_ADDRESS_RE}') AND {key} IN (SELECT contract FROM contract_use)"
    parts = [f"""SELECT {key} AS contract FROM read_parquet({straddle}) o
JOIN {_read(root, 'tx')} t ON t.hash = o.tx_hash JOIN {_read(root, 'block')} b ON b.hash = t.block_hash
WHERE b.slot < {int(first_slot)} AND {match}"""]
    if before:
        parts.append(f"SELECT {key} AS contract FROM read_parquet({before}) o WHERE {match}")
    old = con.execute(f"SELECT count(DISTINCT contract) FROM ({' UNION ALL '.join(parts)})").fetchall()[0][0]
    total = con.execute("SELECT count(*) FROM contract_use").fetchall()[0][0]
    return int(total) - int(old)


def actor_events(con: duckdb.DuckDBPyConnection, root: str, first_slot: int, last_slot: int,
                 manifest: dict) -> ActorEvents:
    """Non-wallet events up to last_slot, and the window's contract use.

    con is a chain_stats.load_window connection for [first_slot, last_slot];
    its paid_output table supplies the window's script outputs.
    """
    _event_slots(con, root, last_slot)
    events = ActorEvents()
    events.pool_events = _rows(con, POOL_EVENTS_SQL, root, ("pool_registration", "certificate"))
    events.drep_events = _rows(con, DREP_EVENTS_SQL, root, ("certificate", "certificate_credential"))
    events.drep_votes = _rows(con, DREP_VOTES_SQL, root, ("vote",))
    events.pool_votes = _rows(con, POOL_VOTES_SQL, root, ("vote",))
    events.proposals = _rows(con, PROPOSALS_SQL, root, ("proposal",))
    con.execute(CONTRACT_USE_SQL)
    events.contract_use = sorted(int(row[0]) for row in con.execute("SELECT txs FROM contract_use").fetchall())
    if events.contract_use:
        events.new_contracts = count_new_contracts(con, root, first_slot, manifest)
    return events
