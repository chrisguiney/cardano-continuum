"""Shared statistics for fit.py and grade.py.

Every statistic is a one-dimensional sample (or a category share) computed
from Parquet tables. Chain tables follow mithril2pq's schema; engine tables
follow continuum's export (internal/sink). The same function names the same
statistic on both sides, so a fit and a grade compare like with like.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field

import duckdb
import numpy as np

QUANTILE_POINTS = 1001

# Statistics compared with a two-sample KS test, in report order.
KS_STATS = [
    "outputs_per_tx",
    "inputs_per_tx",
    "log10_output_value",
    "activity_per_day",
    "log10_gap_seconds",
]
# Share-valued statistics compared by total variation distance.
SHARE_STATS = ["tx_type_mix"]
# Scalar fractions compared by absolute difference.
FRACTION_STATS = ["address_reuse", "change_to_sender"]

TYPES = ["plain", "script", "stake", "pool"]


def _glob(root: str, table: str) -> str:
    return os.path.join(root, table, "*.parquet")


def has_table(root: str, table: str) -> bool:
    d = os.path.join(root, table)
    return os.path.isdir(d) and any(f.endswith(".parquet") for f in os.listdir(d))


@dataclass
class Stats:
    """Samples and shares for one data set. Missing statistics are absent."""

    samples: dict[str, np.ndarray] = field(default_factory=dict)
    shares: dict[str, dict[str, float]] = field(default_factory=dict)
    fractions: dict[str, float] = field(default_factory=dict)
    # Per-address fractions feed the beta fits (competence, care).
    per_address: dict[str, np.ndarray] = field(default_factory=dict)
    info: dict[str, float] = field(default_factory=dict)


# Chain statistics. A transaction's sender is the address of the first
# spent input's source output (smallest address, for determinism). Inputs
# whose source lies outside the loaded range have no sender and drop out of
# the per-address statistics, never out of the per-tx ones.
CHAIN_SQL = """
CREATE OR REPLACE TEMP VIEW c_tx AS
SELECT t.hash, b.slot, t.block_index, t.valid,
       t.script_data_hash IS NOT NULL AS is_script
FROM read_parquet('{tx}') t JOIN read_parquet('{block}') b ON t.block_hash = b.hash;

CREATE OR REPLACE TEMP VIEW c_in AS
SELECT tx_hash, count(*) AS n_in
FROM read_parquet('{tx_input}') WHERE kind = 'spend' GROUP BY tx_hash;

CREATE OR REPLACE TEMP VIEW c_out AS
SELECT tx_hash, count(*) AS n_out
FROM read_parquet('{tx_output}') WHERE NOT is_collateral_return GROUP BY tx_hash;

CREATE OR REPLACE TEMP VIEW c_sender AS
SELECT i.tx_hash, min(o.address) AS sender
FROM read_parquet('{tx_input}') i
JOIN read_parquet('{tx_output}') o
  ON o.tx_hash = i.src_tx_hash AND o.output_index = i.src_index
WHERE i.kind = 'spend' GROUP BY i.tx_hash;

CREATE OR REPLACE TEMP VIEW c_kind AS
SELECT x.hash AS tx_hash,
  CASE WHEN x.is_script THEN 'script'
       WHEN k.pool THEN 'pool'
       WHEN k.stake THEN 'stake'
       ELSE 'plain' END AS kind
FROM c_tx x LEFT JOIN (
  SELECT tx_hash,
         bool_or(cert_type LIKE 'pool_%') AS pool,
         bool_or(cert_type LIKE 'stake_%') AS stake
  FROM {cert} GROUP BY tx_hash) k ON k.tx_hash = x.hash;

CREATE OR REPLACE TEMP VIEW c_facts AS
SELECT x.hash, x.slot, x.block_index, coalesce(i.n_in, 0) AS n_in,
       coalesce(o.n_out, 0) AS n_out, s.sender, k.kind
FROM c_tx x
LEFT JOIN c_in i ON i.tx_hash = x.hash
LEFT JOIN c_out o ON o.tx_hash = x.hash
LEFT JOIN c_sender s ON s.tx_hash = x.hash
JOIN c_kind k ON k.tx_hash = x.hash
WHERE x.valid;
"""

# An output is a reuse when its address appeared on an earlier output or as
# a sender; change_to_sender marks a tx that pays back one of its own inputs'
# addresses.
REUSE_SQL = """
WITH ordered AS (
  SELECT o.address, f.slot, f.block_index, o.output_index,
         row_number() OVER (PARTITION BY o.address
                            ORDER BY f.slot, f.block_index, o.output_index) AS rn
  FROM read_parquet('{tx_output}') o JOIN c_facts f ON f.hash = o.tx_hash
  WHERE NOT o.is_collateral_return)
SELECT avg((rn > 1)::int) FROM ordered
"""

CHANGE_SQL = """
SELECT avg(has_change::int) FROM (
  SELECT f.hash, bool_or(o.address = f.sender) AS has_change
  FROM c_facts f JOIN read_parquet('{tx_output}') o ON o.tx_hash = f.hash
  WHERE f.sender IS NOT NULL AND NOT o.is_collateral_return GROUP BY f.hash)
"""

ADDR_SQL = """
SELECT sender, count(*) AS n,
       (max(slot) - min(slot)) AS span
FROM c_facts WHERE sender IS NOT NULL GROUP BY sender
"""

PER_ADDR_FRAC_SQL = """
SELECT f.sender, avg(CASE WHEN c.hash IS NOT NULL THEN 1 ELSE 0 END) AS chg
FROM c_facts f
LEFT JOIN (
  SELECT DISTINCT f2.hash FROM c_facts f2
  JOIN read_parquet('{tx_output}') o ON o.tx_hash = f2.hash AND o.address = f2.sender
  WHERE NOT o.is_collateral_return) c ON c.hash = f.hash
WHERE f.sender IS NOT NULL GROUP BY f.sender HAVING count(*) >= 3
"""

PER_ADDR_REUSE_SQL = """
WITH ordered AS (
  SELECT f.sender AS sender,
         (row_number() OVER (PARTITION BY o.address
                             ORDER BY f.slot, f.block_index, o.output_index) > 1) AS reuse
  FROM read_parquet('{tx_output}') o JOIN c_facts f ON f.hash = o.tx_hash
  WHERE NOT o.is_collateral_return AND f.sender IS NOT NULL)
SELECT sender, avg(reuse::int) FROM ordered GROUP BY sender HAVING count(*) >= 3
"""

GAP_SQL = """
SELECT slot - lag(slot) OVER (PARTITION BY sender ORDER BY slot, block_index) AS gap
FROM c_facts WHERE sender IS NOT NULL
QUALIFY gap IS NOT NULL
"""


def chain_stats(root: str, slot_seconds: float = 1.0) -> Stats:
    """Statistics of a mithril2pq output directory."""
    con = duckdb.connect()
    p = {t: _glob(root, t) for t in ["tx", "block", "tx_input", "tx_output"]}
    if has_table(root, "certificate"):
        p["cert"] = f"(SELECT tx_hash, cert_type FROM read_parquet('{_glob(root, 'certificate')}'))"
    else:
        p["cert"] = "(SELECT NULL::BLOB AS tx_hash, NULL::VARCHAR AS cert_type WHERE false)"
    for stmt in CHAIN_SQL.format(**p).split(";"):
        if stmt.strip():
            con.execute(stmt)
    st = Stats()
    rows = con.execute("SELECT n_in, n_out, kind FROM c_facts").fetchall()
    n_in = np.array([r[0] for r in rows], dtype=float)
    n_out = np.array([r[1] for r in rows], dtype=float)
    st.samples["inputs_per_tx"] = n_in
    st.samples["outputs_per_tx"] = n_out
    kinds = [r[2] for r in rows]
    total = max(len(kinds), 1)
    st.shares["tx_type_mix"] = {t: kinds.count(t) / total for t in TYPES}

    vals = con.execute(
        f"SELECT lovelace FROM read_parquet('{p['tx_output']}') o "
        "JOIN c_facts f ON f.hash = o.tx_hash "
        "WHERE NOT o.is_collateral_return AND o.lovelace > 0"
    ).fetchall()
    lov = np.array([v[0] for v in vals], dtype=float)
    st.samples["output_value_lovelace"] = lov
    st.samples["log10_output_value"] = np.log10(lov)

    span = con.execute("SELECT max(slot) - min(slot) FROM c_facts").fetchall()[0][0] or 0
    span_days = max(span * slot_seconds / 86400.0, 1e-9)
    addr = con.execute(ADDR_SQL).fetchall()
    counts = np.array([a[1] for a in addr], dtype=float)
    st.samples["tx_count_per_address"] = counts
    st.samples["activity_per_day"] = counts / span_days
    st.info["span_days"] = span_days
    gaps = np.array([g[0] for g in con.execute(GAP_SQL).fetchall()], dtype=float) * slot_seconds
    st.samples["gap_seconds"] = gaps
    st.samples["log10_gap_seconds"] = np.log10(np.maximum(gaps, slot_seconds))

    st.fractions["address_reuse"] = float(con.execute(REUSE_SQL.format(**p)).fetchall()[0][0] or 0.0)
    st.fractions["change_to_sender"] = float(con.execute(CHANGE_SQL.format(**p)).fetchall()[0][0] or 0.0)
    st.per_address["change_to_sender"] = np.array(
        [r[1] for r in con.execute(PER_ADDR_FRAC_SQL.format(**p)).fetchall()], dtype=float
    )
    st.per_address["address_reuse"] = np.array(
        [r[1] for r in con.execute(PER_ADDR_REUSE_SQL.format(**p)).fetchall()], dtype=float
    )
    st.info["txs"] = float(len(kinds))
    return st


def engine_stats(root: str) -> Stats:
    """Statistics of a continuum export directory.

    Actors stand in for addresses and the engine's millisecond clock for
    slots. The engine only submits payments, so the type mix is all plain
    and there is no input or output count without chain tables.
    """
    con = duckdb.connect()
    sub = _glob(root, "submission")
    dec = _glob(root, "decision")
    st = Stats()
    amounts = np.array(
        [r[0] for r in con.execute(f"SELECT amount FROM read_parquet('{sub}') WHERE amount > 0").fetchall()],
        dtype=float,
    )
    st.samples["output_value_lovelace"] = amounts
    st.samples["log10_output_value"] = np.log10(amounts) if len(amounts) else amounts
    span_ms = con.execute(f"SELECT max(\"at\") - min(\"at\") FROM read_parquet('{sub}')").fetchall()[0][0] or 0
    span_days = max(span_ms / 86_400_000.0, 1e-9)
    counts = np.array(
        [r[0] for r in con.execute(f"SELECT count(*) FROM read_parquet('{sub}') GROUP BY actor").fetchall()],
        dtype=float,
    )
    st.samples["tx_count_per_address"] = counts
    st.samples["activity_per_day"] = counts / span_days
    st.info["span_days"] = span_days
    gaps = np.array(
        [
            r[0] / 1000.0
            for r in con.execute(
                f"SELECT \"at\" - lag(\"at\") OVER (PARTITION BY actor ORDER BY \"at\", seq) AS g "
                f"FROM read_parquet('{sub}') QUALIFY g IS NOT NULL"
            ).fetchall()
        ],
        dtype=float,
    )
    st.samples["gap_seconds"] = gaps
    st.samples["log10_gap_seconds"] = np.log10(np.maximum(gaps, 1e-3))
    st.shares["tx_type_mix"] = {"plain": 1.0, "script": 0.0, "stake": 0.0, "pool": 0.0}
    # A payment's target is a reuse when an earlier payment already paid it.
    st.fractions["address_reuse"] = float(
        con.execute(
            f"SELECT avg((rn > 1)::int) FROM (SELECT row_number() OVER "
            f"(PARTITION BY target ORDER BY \"at\", seq) AS rn FROM read_parquet('{sub}'))"
        ).fetchall()[0][0]
        or 0.0
    )
    idle = con.execute(
        f"SELECT count(*) FILTER (WHERE kind = 'IntentChosen' AND intent_kind = 'idle'), "
        f"count(*) FILTER (WHERE kind = 'Tick'), count(*) FROM read_parquet('{dec}')"
    ).fetchall()[0]
    st.info["idle_fraction_of_records"] = (idle[0] + idle[1]) / max(idle[2], 1)
    st.info["payments"] = float(len(amounts))
    return st


def merge_chain(engine: Stats, chain: Stats) -> Stats:
    """Devnet chain tables replace the statistics the engine can only guess at."""
    for k in ("inputs_per_tx", "outputs_per_tx", "log10_output_value", "output_value_lovelace"):
        engine.samples[k] = chain.samples[k]
    engine.shares["tx_type_mix"] = chain.shares["tx_type_mix"]
    engine.fractions.update(chain.fractions)
    return engine


def quantiles(x: np.ndarray, n: int = QUANTILE_POINTS) -> list[float]:
    return [float(v) for v in np.quantile(x, np.linspace(0.0, 1.0, n))]
