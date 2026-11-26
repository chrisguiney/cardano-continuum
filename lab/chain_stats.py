"""Statistics of real chain data (mithril2pq Parquet tables).

This module is the I/O half of fitting: it runs SQL in DuckDB and returns
plain arrays in a population_stats.Stats. No statistics are fitted here.

It works in three steps, each a function:

    load_window     read the transactions of a slot window, work out each
                    one's sender, kind and unit            (tables below)
    scope_to_group  choose which of them to measure: all, or one cluster's
    measure         compute the Stats of the transactions in scope

Tables built by load_window, in order (each SQL step is commented where it
is defined):

    window_tx         valid transactions in the window
    spent_input_count, output_count, tx_sender, tx_kind   one fact per tx
    tx_without_unit   the facts joined: one row per transaction
    seen_address      every address that sent or was paid
    address_unit      address -> unit, filled in from Python (units.py)
    tx_all            tx_without_unit plus the sender's unit
    paid_output       every output with the unit it pays
    tx_in_scope       view: the rows of tx_all being measured

The input tables (tx, block, tx_input, tx_output, certificate) follow
mithril2pq's schema.

Guide: section 6 ("Calibration") and section 7 ("units") of guide.ipynb.
"""

from __future__ import annotations

import os
from typing import Callable

import duckdb
import numpy as np
import pandas as pd

from cardano import SECONDS_PER_DAY
from population_stats import TX_KINDS, Quantity, Stats, sorted_sample

# Columns of tx_all and tx_in_scope: one row per valid transaction.
TX_COLUMNS = (
    Quantity("hash", "bytes", "Transaction hash.", "Joins a transaction to its outputs."),
    Quantity(
        "slot", "slots",
        "Slot of the block that holds the transaction.",
        "The chain's clock: differences between slots are the gaps between a "
        "unit's transactions.",
    ),
    Quantity(
        "block_index", "position",
        "Position of the transaction inside its block.",
        "Orders transactions that share a slot, so 'the previous transaction "
        "of this unit' is well defined.",
    ),
    Quantity("n_in", "count", "Spent inputs.", "The inputs_per_tx sample."),
    Quantity("n_out", "count", "Outputs, without the collateral return.", "The outputs_per_tx sample."),
    Quantity(
        "sender", "address",
        "Address that funded the transaction; NULL when unknown.",
        "A transaction does not name its sender, so it is taken from the "
        "outputs it spends (see the tx_sender step).",
    ),
    Quantity("kind", "category", "plain, script, stake or pool.", "The tx_type_mix shares."),
    Quantity(
        "unit", "unit key",
        "The sender's unit (units.py); NULL when the sender is unknown.",
        "Every per-user statistic groups by it.",
    ),
)

# A unit needs at least this many transactions before its personal change
# and reuse fractions count towards the beta traits. With one or two
# transactions a fraction can only be 0, 1/2 or 1, which says more about the
# count than about the user's habits. Raising it trades noise for fewer
# units; at 3 most occasional users still qualify.
MIN_TXS_FOR_HABIT = 3

# Stands in for "no upper bound" on the slot window.
_NO_MAX_SLOT = 2**62

# An empty window has zero length; this keeps the division by its length
# defined. Any real window is at least many seconds long.
_MIN_SPAN_DAYS = 1e-9

LOAD_WINDOW_SQL = """
-- window_tx: the valid transactions whose block lies in the slot window.
-- A Cardano block can include a transaction whose script failed ("phase 2
-- invalid"): it moves no funds except the forfeited collateral, so it is
-- not user behaviour and is left out. is_script marks a transaction that
-- carries script data, which means it ran a Plutus script.
CREATE OR REPLACE TEMP TABLE window_tx AS
SELECT t.hash, b.slot, t.block_index, t.valid,
       t.script_data_hash IS NOT NULL AS is_script
FROM read_parquet({tx}) t JOIN read_parquet({block}) b ON t.block_hash = b.hash
WHERE b.slot >= {min_slot} AND b.slot <= {max_slot} AND t.valid;

-- spent_input_count: inputs each transaction spends. Only kind 'spend'
-- counts: reference inputs are read without being spent and collateral
-- inputs are spent only when a script fails.
CREATE OR REPLACE TEMP VIEW spent_input_count AS
SELECT i.tx_hash, count(*) AS n_in
FROM read_parquet({tx_input}) i JOIN window_tx x ON x.hash = i.tx_hash
WHERE i.kind = 'spend' GROUP BY i.tx_hash;

-- output_count: outputs each transaction creates. The collateral return is
-- an output that exists only if a script fails, so it is not counted.
CREATE OR REPLACE TEMP VIEW output_count AS
SELECT o.tx_hash, count(*) AS n_out
FROM read_parquet({tx_output}) o JOIN window_tx x ON x.hash = o.tx_hash
WHERE NOT o.is_collateral_return GROUP BY o.tx_hash;

-- tx_sender: who sent each transaction. Cardano transactions have no
-- "from" field. An input points at an earlier output (src_tx_hash,
-- src_index), and the address of that output is who is spending. A
-- transaction can spend from several addresses; the smallest one is taken
-- so the answer does not depend on row order. An input whose source output
-- is older than the loaded data finds no match, and such a transaction has
-- no sender.
CREATE OR REPLACE TEMP VIEW tx_sender AS
SELECT i.tx_hash, min(o.address) AS sender
FROM read_parquet({tx_input}) i
JOIN window_tx x ON x.hash = i.tx_hash
JOIN read_parquet({tx_output}) o
  ON o.tx_hash = i.src_tx_hash AND o.output_index = i.src_index
WHERE i.kind = 'spend' GROUP BY i.tx_hash;

-- tx_kind: what each transaction was for. The first rule that applies
-- wins: script, then pool certificate, then stake certificate, else plain.
CREATE OR REPLACE TEMP VIEW tx_kind AS
SELECT x.hash AS tx_hash,
  CASE WHEN x.is_script THEN 'script'
       WHEN k.pool THEN 'pool'
       WHEN k.stake THEN 'stake'
       ELSE 'plain' END AS kind
FROM window_tx x LEFT JOIN (
  SELECT tx_hash,
         bool_or(cert_type LIKE 'pool_%') AS pool,
         bool_or(cert_type LIKE 'stake_%') AS stake
  FROM {certificate} GROUP BY tx_hash) k ON k.tx_hash = x.hash;

-- tx_without_unit: the facts above side by side, one row per transaction.
-- A transaction with no spent inputs or no counted outputs gets a count
-- of zero instead of dropping out.
CREATE OR REPLACE TEMP TABLE tx_without_unit AS
SELECT x.hash, x.slot, x.block_index, coalesce(i.n_in, 0) AS n_in,
       coalesce(o.n_out, 0) AS n_out, s.sender, k.kind
FROM window_tx x
LEFT JOIN spent_input_count i ON i.tx_hash = x.hash
LEFT JOIN output_count o ON o.tx_hash = x.hash
LEFT JOIN tx_sender s ON s.tx_hash = x.hash
JOIN tx_kind k ON k.tx_hash = x.hash;

-- seen_address: every address that sent a transaction or was paid by one.
-- Python maps each to its unit, because decoding an address is easier
-- there than in SQL.
CREATE OR REPLACE TEMP TABLE seen_address AS
SELECT DISTINCT address FROM (
  SELECT sender AS address FROM tx_without_unit WHERE sender IS NOT NULL
  UNION ALL
  SELECT o.address FROM read_parquet({tx_output}) o JOIN window_tx x ON x.hash = o.tx_hash
  WHERE NOT o.is_collateral_return);
"""

ATTACH_UNITS_SQL = """
-- tx_all: each transaction with its sender's unit. An address missing from
-- address_unit is its own unit.
CREATE OR REPLACE TEMP TABLE tx_all AS
SELECT b.*, coalesce(u.unit, b.sender) AS unit
FROM tx_without_unit b LEFT JOIN address_unit u ON u.address = b.sender;

-- paid_output: every counted output of the loaded transactions, with
--   paid_unit  the unit of the address it pays, and
--   is_reuse   whether an earlier output in the window paid the same
--              address (outputs are numbered per address in chain order,
--              and every one after the first is a reuse).
CREATE OR REPLACE TEMP TABLE paid_output AS
SELECT f.hash, o.address, coalesce(u.unit, o.address) AS paid_unit, o.lovelace,
       (row_number() OVER (PARTITION BY o.address
                           ORDER BY f.slot, f.block_index, o.output_index) > 1) AS is_reuse
FROM tx_all f
JOIN read_parquet({tx_output}) o ON o.tx_hash = f.hash
LEFT JOIN address_unit u ON u.address = o.address
WHERE NOT o.is_collateral_return;
"""

TX_SHAPE_SQL = "SELECT n_in, n_out, kind FROM tx_in_scope"

# The size of each payment, EXCLUDING CHANGE. A Cardano output must be spent
# whole, so a wallet paying 10 ADA from a 500 ADA output creates two
# outputs: 10 ADA to the payee and about 490 ADA back to itself. That
# second output is change. It says how much the wallet held, not how much
# it paid, and counting it would roughly double the payments and fill the
# sample with balances. An output is change when it pays the sender's own
# unit. When the sender is unknown every output is kept.
PAYMENT_SQL = """
SELECT o.lovelace FROM paid_output o JOIN tx_in_scope f ON f.hash = o.hash
WHERE o.lovelace > 0 AND (f.unit IS NULL OR o.paid_unit <> f.unit)
"""

SPAN_SLOTS_SQL = "SELECT max(slot) - min(slot) FROM tx_in_scope"

# Transactions sent by each unit.
TX_COUNT_SQL = """
SELECT unit, count(*) AS n FROM tx_in_scope WHERE unit IS NOT NULL GROUP BY unit
"""

# Average number of distinct days on which a unit sent a transaction. The
# parameter is the slot length in seconds: slot * seconds / 86400 is the
# day number.
ACTIVE_DAYS_SQL = """
WITH unit_days AS (
  SELECT unit, count(DISTINCT floor(slot * ? / 86400)) AS d
  FROM tx_in_scope WHERE unit IS NOT NULL GROUP BY unit)
SELECT avg(d) FROM unit_days
"""

# Slots between each transaction and the same unit's previous one. A unit's
# first transaction has no previous one and yields no gap.
GAP_SQL = """
SELECT slot - lag(slot) OVER (PARTITION BY unit ORDER BY slot, block_index) AS gap
FROM tx_in_scope WHERE unit IS NOT NULL
QUALIFY gap IS NOT NULL
"""

# Share of outputs that pay an address already paid earlier in the window.
REUSE_SQL = """
SELECT avg(o.is_reuse::int) FROM paid_output o JOIN tx_in_scope f ON f.hash = o.hash
"""

# Share of transactions that pay change back to their sender's unit.
CHANGE_SQL = """
WITH tx_change AS (
  SELECT f.hash, bool_or(o.paid_unit = f.unit) AS has_change
  FROM tx_in_scope f JOIN paid_output o ON o.hash = f.hash
  WHERE f.unit IS NOT NULL GROUP BY f.hash)
SELECT avg(has_change::int) FROM tx_change
"""

# The same change share, per unit: one fraction for each unit with at least
# {min_txs} transactions.
UNIT_CHANGE_SQL = """
WITH tx_with_change AS (
  SELECT DISTINCT f2.hash FROM tx_in_scope f2
  JOIN paid_output o ON o.hash = f2.hash AND o.paid_unit = f2.unit)
SELECT f.unit, avg(CASE WHEN c.hash IS NOT NULL THEN 1 ELSE 0 END) AS chg
FROM tx_in_scope f
LEFT JOIN tx_with_change c ON c.hash = f.hash
WHERE f.unit IS NOT NULL GROUP BY f.unit HAVING count(*) >= {min_txs}
"""

# The same reuse share, per unit, over the outputs of its transactions. The
# threshold counts output rows here, not transactions.
UNIT_REUSE_SQL = """
SELECT f.unit, avg(o.is_reuse::int) FROM paid_output o JOIN tx_in_scope f ON f.hash = o.hash
WHERE f.unit IS NOT NULL GROUP BY f.unit HAVING count(*) >= {min_txs}
"""


def sql_text(value: str) -> str:
    """A string as a quoted SQL literal. DuckDB cannot take a file path or a
    setting as a query parameter, so those are quoted and escaped here."""
    return "'" + value.replace("'", "''") + "'"


def table_glob(root: str, table: str) -> str:
    """The Parquet files of one table under a data directory."""
    return os.path.join(root, table, "*.parquet")


def has_table(root: str, table: str) -> bool:
    """True when the data directory holds at least one file of the table."""
    directory = os.path.join(root, table)
    return os.path.isdir(directory) and any(name.endswith(".parquet") for name in os.listdir(directory))


def _table_sources(root: str) -> dict[str, str]:
    """SQL source of every input table, keyed by the placeholder that names
    it in the SQL above. A chain with no certificates at all (a short devnet)
    has no certificate table, so an empty one stands in."""
    sources = {table: sql_text(table_glob(root, table)) for table in ["tx", "block", "tx_input", "tx_output"]}
    if has_table(root, "certificate"):
        files = sql_text(table_glob(root, "certificate"))
        sources["certificate"] = f"(SELECT tx_hash, cert_type FROM read_parquet({files}))"
    else:
        sources["certificate"] = "(SELECT NULL::BLOB AS tx_hash, NULL::VARCHAR AS cert_type WHERE false)"
    return sources


def _first_value(con: duckdb.DuckDBPyConnection, sql: str, params: list | None = None):
    """The single value of a one-row, one-column query."""
    return con.execute(sql, params or []).fetchall()[0][0]


def _column(con: duckdb.DuckDBPyConnection, sql: str, index: int = 0) -> np.ndarray:
    """One column of a query as a sorted sample."""
    return sorted_sample(row[index] for row in con.execute(sql).fetchall())


def last_slot(root: str) -> int:
    """Slot of the newest block that carries a transaction."""
    blocks = sql_text(table_glob(root, "block"))
    con = duckdb.connect()
    return int(_first_value(con, f"SELECT max(slot) FROM read_parquet({blocks}) WHERE tx_count > 0"))


def load_window(
    root: str,
    min_slot: int = 0,
    max_slot: int = _NO_MAX_SLOT,
    unit_of: Callable[[list[str]], list[str]] | None = None,
    memory_limit: str | None = None,
) -> duckdb.DuckDBPyConnection:
    """Load the transactions in slots [min_slot, max_slot] into a connection.

    unit_of maps a list of addresses to their units (units.unit_of); None
    makes every address its own unit. The returned connection holds the
    tables listed in the module docstring, scoped to every transaction.
    """
    con = duckdb.connect()
    if memory_limit:
        con.execute(f"SET memory_limit={sql_text(memory_limit)}")
    sources = _table_sources(root)
    con.execute(LOAD_WINDOW_SQL.format(min_slot=int(min_slot), max_slot=int(max_slot), **sources))
    _store_address_units(con, unit_of)
    con.execute(ATTACH_UNITS_SQL.format(**sources))
    scope_to_group(con)
    return con


def _store_address_units(con: duckdb.DuckDBPyConnection, unit_of: Callable[[list[str]], list[str]] | None) -> None:
    """Fill address_unit(address, unit) for every seen address. With no
    unit_of the table stays empty, which makes each address its own unit."""
    con.execute("CREATE OR REPLACE TEMP TABLE address_unit (address VARCHAR, unit VARCHAR)")
    if unit_of is None:
        return
    rows = con.execute("SELECT address FROM seen_address ORDER BY address").fetchall()
    addresses = [row[0] for row in rows]
    if addresses:
        con.executemany("INSERT INTO address_unit VALUES (?, ?)", list(zip(addresses, unit_of(addresses))))


def set_unit_groups(con: duckdb.DuckDBPyConnection, units: list[str], groups: list[str]) -> None:
    """Record which group (cluster) each unit belongs to, for scope_to_group."""
    con.register("unit_group_rows", pd.DataFrame({"unit": units, "group_name": groups}))
    con.execute("CREATE OR REPLACE TEMP TABLE unit_group AS SELECT * FROM unit_group_rows")


def scope_to_group(con: duckdb.DuckDBPyConnection, group: str | None = None) -> None:
    """Point tx_in_scope at every loaded transaction, or at those sent by the
    units of one group (see set_unit_groups). measure reads tx_in_scope."""
    if group is None:
        con.execute("CREATE OR REPLACE TEMP VIEW tx_in_scope AS SELECT * FROM tx_all")
        return
    con.execute(
        "CREATE OR REPLACE TEMP VIEW tx_in_scope AS SELECT b.* FROM tx_all b "
        f"JOIN unit_group g ON g.unit = b.unit WHERE g.group_name = {sql_text(group)}"
    )


def kind_shares(kinds: list[str]) -> dict[str, float]:
    """Share of each transaction kind; all zero for no transactions."""
    total = max(len(kinds), 1)
    return {kind: kinds.count(kind) / total for kind in TX_KINDS}


def _span_days(con: duckdb.DuckDBPyConnection, slot_seconds: float, span_seconds: float | None) -> float:
    """Length of the observation window in days. With no span given it is the
    time from the first to the last transaction in scope."""
    if span_seconds is None:
        span_slots = _first_value(con, SPAN_SLOTS_SQL) or 0
        span_seconds = span_slots * slot_seconds
    return max(span_seconds / float(SECONDS_PER_DAY), _MIN_SPAN_DAYS)


def _active_fraction(con: duckdb.DuckDBPyConnection, slot_seconds: float, span_days: float) -> float:
    """Share of the window's days on which an average unit sent something.

    A unit active on 9 days of a 90-day window has a fraction of 0.1. The
    fit treats the remaining 90% as dormancy. With no units the fraction is
    taken as one day's worth.
    """
    mean_active_days = _first_value(con, ACTIVE_DAYS_SQL, [slot_seconds])
    return min(float(mean_active_days or 1.0) / span_days, 1.0)


def measure(
    con: duckdb.DuckDBPyConnection, slot_seconds: float = 1.0, span_seconds: float | None = None
) -> Stats:
    """Statistics of the transactions tx_in_scope currently selects.

    slot_seconds converts slot differences to seconds. span_seconds is the
    length of the observation window; None means the span of the selected
    transactions themselves.
    """
    stats = Stats()
    shape_rows = con.execute(TX_SHAPE_SQL).fetchall()
    stats.samples["inputs_per_tx"] = sorted_sample(row[0] for row in shape_rows)
    stats.samples["outputs_per_tx"] = sorted_sample(row[1] for row in shape_rows)
    stats.shares["tx_type_mix"] = kind_shares([row[2] for row in shape_rows])

    payments = _column(con, PAYMENT_SQL)
    stats.samples["output_value_lovelace"] = payments
    stats.samples["log10_output_value"] = np.log10(payments)

    span_days = _span_days(con, slot_seconds, span_seconds)
    tx_counts = _column(con, TX_COUNT_SQL, index=1)
    stats.samples["tx_count_per_address"] = tx_counts
    stats.samples["activity_per_day"] = tx_counts / span_days
    stats.info["span_days"] = span_days
    stats.info["active_fraction"] = _active_fraction(con, slot_seconds, span_days)

    gaps = _column(con, GAP_SQL) * slot_seconds
    stats.samples["gap_seconds"] = gaps
    # Two transactions in one slot are zero seconds apart; one slot is the
    # finest the chain's clock resolves, so that is the floor before the log.
    stats.samples["log10_gap_seconds"] = np.log10(np.maximum(gaps, slot_seconds))

    stats.fractions["address_reuse"] = float(_first_value(con, REUSE_SQL) or 0.0)
    stats.fractions["change_to_sender"] = float(_first_value(con, CHANGE_SQL) or 0.0)
    stats.per_address["change_to_sender"] = _column(con, UNIT_CHANGE_SQL.format(min_txs=MIN_TXS_FOR_HABIT), index=1)
    stats.per_address["address_reuse"] = _column(con, UNIT_REUSE_SQL.format(min_txs=MIN_TXS_FOR_HABIT), index=1)
    stats.info["txs"] = float(len(shape_rows))
    return stats


def chain_stats(root: str, slot_seconds: float = 1.0) -> Stats:
    """Statistics of a whole mithril2pq output directory, one unit per address."""
    return measure(load_window(root), slot_seconds)
