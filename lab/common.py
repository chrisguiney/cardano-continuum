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
# the per-address statistics, never out of the per-tx ones. A unit is the
# stake credential of the sender where it has one (see units.py) and the
# sender address otherwise; every per-unit statistic groups by unit.
CHAIN_SQL = """
CREATE OR REPLACE TEMP TABLE c_tx AS
SELECT t.hash, b.slot, t.block_index, t.valid,
       t.script_data_hash IS NOT NULL AS is_script
FROM read_parquet('{tx}') t JOIN read_parquet('{block}') b ON t.block_hash = b.hash
WHERE b.slot >= {min_slot} AND b.slot <= {max_slot} AND t.valid;

CREATE OR REPLACE TEMP VIEW c_in AS
SELECT i.tx_hash, count(*) AS n_in
FROM read_parquet('{tx_input}') i JOIN c_tx x ON x.hash = i.tx_hash
WHERE i.kind = 'spend' GROUP BY i.tx_hash;

CREATE OR REPLACE TEMP VIEW c_out AS
SELECT o.tx_hash, count(*) AS n_out
FROM read_parquet('{tx_output}') o JOIN c_tx x ON x.hash = o.tx_hash
WHERE NOT o.is_collateral_return GROUP BY o.tx_hash;

CREATE OR REPLACE TEMP VIEW c_sender AS
SELECT i.tx_hash, min(o.address) AS sender
FROM read_parquet('{tx_input}') i
JOIN c_tx x ON x.hash = i.tx_hash
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

CREATE OR REPLACE TEMP TABLE c_base0 AS
SELECT x.hash, x.slot, x.block_index, coalesce(i.n_in, 0) AS n_in,
       coalesce(o.n_out, 0) AS n_out, s.sender, k.kind
FROM c_tx x
LEFT JOIN c_in i ON i.tx_hash = x.hash
LEFT JOIN c_out o ON o.tx_hash = x.hash
LEFT JOIN c_sender s ON s.tx_hash = x.hash
JOIN c_kind k ON k.tx_hash = x.hash;

CREATE OR REPLACE TEMP TABLE c_addr AS
SELECT DISTINCT address FROM (
  SELECT sender AS address FROM c_base0 WHERE sender IS NOT NULL
  UNION ALL
  SELECT o.address FROM read_parquet('{tx_output}') o JOIN c_tx x ON x.hash = o.tx_hash
  WHERE NOT o.is_collateral_return);
"""

# c_unit maps an address to its unit; addresses it omits are their own unit.
# c_outs holds every non-collateral output of the loaded transactions with the
# unit it pays and whether its address was seen earlier in the window.
UNIT_SQL = """
CREATE OR REPLACE TEMP TABLE c_base AS
SELECT b.*, coalesce(u.unit, b.sender) AS unit
FROM c_base0 b LEFT JOIN c_unit u ON u.address = b.sender;

CREATE OR REPLACE TEMP TABLE c_outs AS
SELECT f.hash, o.address, coalesce(u.unit, o.address) AS ounit, o.lovelace,
       (row_number() OVER (PARTITION BY o.address
                           ORDER BY f.slot, f.block_index, o.output_index) > 1) AS reuse
FROM c_base f
JOIN read_parquet('{tx_output}') o ON o.tx_hash = f.hash
LEFT JOIN c_unit u ON u.address = o.address
WHERE NOT o.is_collateral_return;
"""

REUSE_SQL = """
SELECT avg(o.reuse::int) FROM c_outs o JOIN c_facts f ON f.hash = o.hash
"""

# change_to_sender marks a tx that pays back its own sender's unit.
CHANGE_SQL = """
SELECT avg(has_change::int) FROM (
  SELECT f.hash, bool_or(o.ounit = f.unit) AS has_change
  FROM c_facts f JOIN c_outs o ON o.hash = f.hash
  WHERE f.unit IS NOT NULL GROUP BY f.hash)
"""

# A payment is an output that leaves the sender's unit; change is not one.
PAYMENT_SQL = """
SELECT o.lovelace FROM c_outs o JOIN c_facts f ON f.hash = o.hash
WHERE o.lovelace > 0 AND (f.unit IS NULL OR o.ounit <> f.unit)
"""

ADDR_SQL = """
SELECT unit, count(*) AS n FROM c_facts WHERE unit IS NOT NULL GROUP BY unit
"""

PER_ADDR_FRAC_SQL = """
SELECT f.unit, avg(CASE WHEN c.hash IS NOT NULL THEN 1 ELSE 0 END) AS chg
FROM c_facts f
LEFT JOIN (
  SELECT DISTINCT f2.hash FROM c_facts f2
  JOIN c_outs o ON o.hash = f2.hash AND o.ounit = f2.unit) c ON c.hash = f.hash
WHERE f.unit IS NOT NULL GROUP BY f.unit HAVING count(*) >= 3
"""

PER_ADDR_REUSE_SQL = """
SELECT f.unit, avg(o.reuse::int) FROM c_outs o JOIN c_facts f ON f.hash = o.hash
WHERE f.unit IS NOT NULL GROUP BY f.unit HAVING count(*) >= 3
"""

GAP_SQL = """
SELECT slot - lag(slot) OVER (PARTITION BY unit ORDER BY slot, block_index) AS gap
FROM c_facts WHERE unit IS NOT NULL
QUALIFY gap IS NOT NULL
"""


def _tables(root: str) -> dict[str, str]:
    p = {t: _glob(root, t) for t in ["tx", "block", "tx_input", "tx_output"]}
    if has_table(root, "certificate"):
        p["cert"] = f"(SELECT tx_hash, cert_type FROM read_parquet('{_glob(root, 'certificate')}'))"
    else:
        p["cert"] = "(SELECT NULL::BLOB AS tx_hash, NULL::VARCHAR AS cert_type WHERE false)"
    return p


def last_slot(root: str) -> int:
    """Slot of the newest block that carries a transaction."""
    con = duckdb.connect()
    return int(
        con.execute(
            f"SELECT max(slot) FROM read_parquet('{_glob(root, 'block')}') WHERE tx_count > 0"
        ).fetchall()[0][0]
    )


def prepare(
    root: str,
    min_slot: int = 0,
    max_slot: int = 2**62,
    unit_of=None,
    memory_limit: str | None = None,
) -> duckdb.DuckDBPyConnection:
    """Load the transactions in [min_slot, max_slot] into a connection.

    unit_of maps a list of addresses to their units (see units.py); None makes
    every address its own unit. The connection holds the facts that
    scope() and stats_from() read.
    """
    con = duckdb.connect()
    if memory_limit:
        con.execute(f"SET memory_limit='{memory_limit}'")
    p = _tables(root)
    for stmt in CHAIN_SQL.format(min_slot=min_slot, max_slot=max_slot, **p).split(";"):
        if stmt.strip():
            con.execute(stmt)
    if unit_of is None:
        addrs, units = [], []
    else:
        addrs = [r[0] for r in con.execute("SELECT address FROM c_addr ORDER BY address").fetchall()]
        units = unit_of(addrs)
    con.execute("CREATE OR REPLACE TEMP TABLE c_unit (address VARCHAR, unit VARCHAR)")
    if addrs:
        con.executemany("INSERT INTO c_unit VALUES (?, ?)", list(zip(addrs, units)))
    for stmt in UNIT_SQL.format(**p).split(";"):
        if stmt.strip():
            con.execute(stmt)
    scope(con)
    return con


def scope(con: duckdb.DuckDBPyConnection, group: str | None = None) -> None:
    """Point c_facts at every loaded transaction, or at one group's.

    Groups come from a c_group(unit, grp) table the caller registers.
    """
    if group is None:
        con.execute("CREATE OR REPLACE TEMP VIEW c_facts AS SELECT * FROM c_base")
        return
    quoted = group.replace("'", "''")
    con.execute(
        "CREATE OR REPLACE TEMP VIEW c_facts AS SELECT b.* FROM c_base b "
        f"JOIN c_group g ON g.unit = b.unit WHERE g.grp = '{quoted}'"
    )


def stats_from(con: duckdb.DuckDBPyConnection, slot_seconds: float = 1.0, span_seconds: float | None = None) -> Stats:
    """Statistics of the transactions c_facts currently selects.

    span_seconds is the observation window; None means the span of the
    selected transactions themselves.
    """
    st = Stats()
    rows = con.execute("SELECT n_in, n_out, kind FROM c_facts").fetchall()
    n_in = np.array([r[0] for r in rows], dtype=float)
    n_out = np.array([r[1] for r in rows], dtype=float)
    st.samples["inputs_per_tx"] = n_in
    st.samples["outputs_per_tx"] = n_out
    kinds = [r[2] for r in rows]
    total = max(len(kinds), 1)
    st.shares["tx_type_mix"] = {t: kinds.count(t) / total for t in TYPES}

    lov = np.array([v[0] for v in con.execute(PAYMENT_SQL).fetchall()], dtype=float)
    st.samples["output_value_lovelace"] = lov
    st.samples["log10_output_value"] = np.log10(lov)

    if span_seconds is None:
        span = con.execute("SELECT max(slot) - min(slot) FROM c_facts").fetchall()[0][0] or 0
        span_seconds = span * slot_seconds
    span_days = max(span_seconds / 86400.0, 1e-9)
    counts = np.array([a[1] for a in con.execute(ADDR_SQL).fetchall()], dtype=float)
    st.samples["tx_count_per_address"] = counts
    st.samples["activity_per_day"] = counts / span_days
    st.info["span_days"] = span_days
    # Share of the window an average unit is active in, by days with a tx: the
    # time a unit spends dormant is the rest.
    act = con.execute(
        "SELECT avg(d) FROM (SELECT unit, count(DISTINCT floor(slot * ? / 86400)) AS d "
        "FROM c_facts WHERE unit IS NOT NULL GROUP BY unit)", [slot_seconds]
    ).fetchall()[0][0]
    st.info["active_fraction"] = min(float(act or 1.0) / span_days, 1.0)
    gaps = np.array([g[0] for g in con.execute(GAP_SQL).fetchall()], dtype=float) * slot_seconds
    st.samples["gap_seconds"] = gaps
    st.samples["log10_gap_seconds"] = np.log10(np.maximum(gaps, slot_seconds))

    st.fractions["address_reuse"] = float(con.execute(REUSE_SQL).fetchall()[0][0] or 0.0)
    st.fractions["change_to_sender"] = float(con.execute(CHANGE_SQL).fetchall()[0][0] or 0.0)
    st.per_address["change_to_sender"] = np.array([r[1] for r in con.execute(PER_ADDR_FRAC_SQL).fetchall()], dtype=float)
    st.per_address["address_reuse"] = np.array([r[1] for r in con.execute(PER_ADDR_REUSE_SQL).fetchall()], dtype=float)
    st.info["txs"] = float(len(kinds))
    return st


def chain_stats(root: str, slot_seconds: float = 1.0) -> Stats:
    """Statistics of a mithril2pq output directory."""
    return stats_from(prepare(root), slot_seconds)


def run_profile_hash(root: str) -> str:
    """Hex profile hash from the run table of a continuum export directory."""
    con = duckdb.connect()
    rows = con.execute(f"SELECT hex(profile_hash) FROM read_parquet('{_glob(root, 'run')}')").fetchall()
    return rows[0][0].lower()


def actor_profiles(root: str, store: str | None = None) -> dict[int, str]:
    """Profile name of every actor of a run.

    Genesis actors have no Spawned record in the log, so their profiles come
    from the run's store.db (store); actors born later are in the export's
    actor_transition table.
    """
    out: dict[int, str] = {}
    if store and os.path.exists(store):
        import sqlite3

        db = sqlite3.connect(f"file:{store}?mode=ro&immutable=1", uri=True)
        try:
            out.update({int(i): p for i, p in db.execute("SELECT id, profile FROM actor") if p})
        finally:
            db.close()
    if has_table(root, "actor_transition"):
        con = duckdb.connect()
        for a, p in con.execute(
            f"SELECT actor, profile FROM read_parquet('{_glob(root, 'actor_transition')}') WHERE profile IS NOT NULL"
        ).fetchall():
            out[int(a)] = p
    return out


def engine_stats(root: str, actors: set[int] | None = None) -> Stats:
    """Statistics of a continuum export directory.

    actors, when given, restricts them to those actor IDs.

    Actors stand in for addresses and the engine's millisecond clock for
    slots. The engine only submits payments, so the type mix is all plain
    and there is no input or output count without chain tables.
    """
    con = duckdb.connect()
    where = ""
    if actors is not None:
        ids = ", ".join(str(int(a)) for a in sorted(actors)) or "NULL"
        where = f" WHERE actor IN ({ids})"
    for view, table in (("sub", "submission"), ("dec", "decision")):
        con.execute(f"CREATE TEMP VIEW {view} AS SELECT * FROM read_parquet('{_glob(root, table)}'){where}")
    sub, dec = "sub", "dec"
    st = Stats()
    amounts = np.array(
        [r[0] for r in con.execute(f"SELECT amount FROM {sub} WHERE amount > 0").fetchall()],
        dtype=float,
    )
    st.samples["output_value_lovelace"] = amounts
    st.samples["log10_output_value"] = np.log10(amounts) if len(amounts) else amounts
    span_ms = con.execute(f"SELECT max(\"at\") - min(\"at\") FROM {sub}").fetchall()[0][0] or 0
    span_days = max(span_ms / 86_400_000.0, 1e-9)
    counts = np.array(
        [r[0] for r in con.execute(f"SELECT count(*) FROM {sub} GROUP BY actor").fetchall()],
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
                f"FROM {sub} QUALIFY g IS NOT NULL"
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
            f"(PARTITION BY target ORDER BY \"at\", seq) AS rn FROM {sub})"
        ).fetchall()[0][0]
        or 0.0
    )
    idle = con.execute(
        f"SELECT count(*) FILTER (WHERE kind = 'IntentChosen' AND intent_kind = 'idle'), "
        f"count(*) FILTER (WHERE kind = 'Tick'), count(*) FROM {dec}"
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
