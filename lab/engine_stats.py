"""Statistics of a simulated run (`continuum export` Parquet tables).

The engine side of grading: the same named statistics as chain_stats, read
from what the simulator decided to do instead of from a chain.

Two things differ from the chain, and both are by construction:

  * An **actor** stands in for a unit. The engine knows exactly which actor
    sent each payment, so there is no sender or unit to work out.
  * Time is the engine's own clock in **milliseconds** (the `at` column),
    not slots, so durations are divided by 1000 to get seconds.

Without the devnet's own chain tables (population_stats.with_chain_tables)
there are no input or output counts and no change, and the transaction
mix is read from the intent behind each submission (INTENT_TX_KIND).

Tables read (continuum's export schema, internal/sink):
    submission  one row per payment the engine submitted:
                actor, target (payee), amount (lovelace), at (ms), seq
    decision    one row per decision record: kind, intent_kind
    run         one row: profile_hash
    actor_transition  lifecycle changes, with the profile of actors born
                during the run

Guide: section 6 of guide.ipynb ("Calibration: fit, run, grade").
"""

from __future__ import annotations

import math
import os
from dataclasses import dataclass, field
import sqlite3

import duckdb
import numpy as np

from cardano import SECONDS_PER_DAY
from chain_stats import has_table, sql_text, table_glob
from population_stats import TX_KINDS, Stats, sorted_sample

MS_PER_SECOND = 1000.0
MS_PER_DAY = SECONDS_PER_DAY * MS_PER_SECOND

# The engine's clock resolves one millisecond, so that is the smallest gap
# it can record and the floor before taking a logarithm.
MIN_GAP_SECONDS = 1e-3

# Keeps a rate defined for a run with a single payment (zero-length span).
_MIN_SPAN_DAYS = 1e-9

# The chain transaction kind (population_stats.TX_KINDS) each engine intent
# produces, by the rule chain_stats.tx_kind applies to the transaction the
# planner builds for it. A withdrawal carries no certificate, so on chain it
# is plain.
INTENT_TX_KIND = {
    "send": "plain", "withdraw_rewards": "plain",
    "script_call": "script", "mint": "script", "burn": "script",
    "delegate": "stake", "undelegate": "stake", "register_stake": "stake",
    "register_pool": "pool", "retire_pool": "pool", "update_pool": "pool",
    "vote": "gov", "retire_drep": "gov", "register_drep": "gov", "delegate_drep": "gov", "propose": "gov",
}

# Intents submitted, by kind. A Submitted record names its intent but not the
# kind; the actor's IntentChosen record just before the intent's first
# submission does. Retries resubmit the same intent, so each intent counts
# once, as its transaction does on chain.
SUBMITTED_INTENTS_SQL = """
WITH first_submit AS (
  SELECT intent_id, actor, min(seq) AS seq FROM decision WHERE kind = 'Submitted' GROUP BY intent_id, actor),
chosen AS (SELECT actor, seq, intent_kind FROM decision WHERE kind = 'IntentChosen')
SELECT c.intent_kind, count(*) FROM first_submit s
ASOF JOIN chosen c ON s.actor = c.actor AND s.seq >= c.seq
GROUP BY c.intent_kind ORDER BY c.intent_kind
"""

AMOUNT_SQL = "SELECT amount FROM submission WHERE amount > 0"

SPAN_MS_SQL = 'SELECT max("at") - min("at") FROM submission'

PAYMENTS_PER_ACTOR_SQL = "SELECT count(*) FROM submission GROUP BY actor"

# Milliseconds between each payment and the same actor's previous one.
GAP_MS_SQL = """
SELECT "at" - lag("at") OVER (PARTITION BY actor ORDER BY "at", seq) AS g
FROM submission QUALIFY g IS NOT NULL
"""

# A payment's target is a reuse when an earlier payment already paid it:
# payments are numbered per target in time order, and every one after the
# first is a reuse.
REUSE_SQL = """
WITH numbered AS (
  SELECT row_number() OVER (PARTITION BY target ORDER BY "at", seq) AS rn FROM submission)
SELECT avg((rn > 1)::int) FROM numbered
"""

# How many decisions were to do nothing. An actor that wakes up and decides
# to stay idle logs IntentChosen with intent 'idle'; a Tick is a wake-up
# with nothing to decide. The third column is every decision record.
IDLE_SQL = """
SELECT count(*) FILTER (WHERE kind = 'IntentChosen' AND intent_kind = 'idle'),
       count(*) FILTER (WHERE kind = 'Tick'),
       count(*)
FROM decision
"""


def run_profile_hash(root: str) -> str:
    """Hex profile hash from the run table of a continuum export directory.

    It identifies the exact profile file the run used, which is how a grade
    finds the matching target.
    """
    runs = sql_text(table_glob(root, "run"))
    rows = duckdb.connect().execute(f"SELECT hex(profile_hash) FROM read_parquet({runs})").fetchall()
    return rows[0][0].lower()


def actor_profiles(root: str, store: str | None = None) -> dict[int, str]:
    """Profile name of every actor of a run.

    Genesis actors have no Spawned record in the log, so their profiles come
    from the run's store.db (store); actors born later are in the export's
    actor_transition table and override the store.
    """
    profiles: dict[int, str] = {}
    if store and os.path.exists(store):
        profiles.update(_genesis_actor_profiles(store))
    if has_table(root, "actor_transition"):
        profiles.update(_born_actor_profiles(root))
    return profiles


def _genesis_actor_profiles(store: str) -> dict[int, str]:
    """Actor id to profile name from a run's SQLite store, opened read-only
    and immutable so a store that a run is still writing is not locked."""
    db = sqlite3.connect(f"file:{store}?mode=ro&immutable=1", uri=True)
    try:
        return {int(actor): profile for actor, profile in db.execute("SELECT id, profile FROM actor") if profile}
    finally:
        db.close()


def _born_actor_profiles(root: str) -> dict[int, str]:
    """Actor id to profile name for actors spawned during the run."""
    transitions = sql_text(table_glob(root, "actor_transition"))
    rows = duckdb.connect().execute(
        f"SELECT actor, profile FROM read_parquet({transitions}) WHERE profile IS NOT NULL"
    ).fetchall()
    return {int(actor): profile for actor, profile in rows}


def _open_export(root: str, actors: set[int] | None) -> duckdb.DuckDBPyConnection:
    """A connection with submission and decision views over the export,
    restricted to the given actor ids when there are any. An empty set
    selects nothing (`IN (NULL)` matches no row)."""
    con = duckdb.connect()
    only_actors = ""
    if actors is not None:
        ids = ", ".join(str(int(actor)) for actor in sorted(actors)) or "NULL"
        only_actors = f" WHERE actor IN ({ids})"
    for table in ("submission", "decision"):
        files = sql_text(table_glob(root, table))
        con.execute(f"CREATE TEMP VIEW {table} AS SELECT * FROM read_parquet({files}){only_actors}")
    return con


def _column(con: duckdb.DuckDBPyConnection, sql: str) -> np.ndarray:
    """The first column of a query as a sorted sample."""
    return sorted_sample(row[0] for row in con.execute(sql).fetchall())


def _first_value(con: duckdb.DuckDBPyConnection, sql: str):
    """The single value of a one-row, one-column query."""
    return con.execute(sql).fetchall()[0][0]


def idle_fraction(idle_choices: int, ticks: int, records: int) -> float:
    """Share of decision records that led to no action."""
    return (idle_choices + ticks) / max(records, 1)


def type_mix(intent_counts: list[tuple[str, int]]) -> dict[str, float]:
    """tx_type_mix of a run from its submissions per intent. A run whose
    submissions carry no known intent counts as all plain, which is what
    the engine submitted before it had other intents."""
    counts = {kind: 0 for kind in TX_KINDS}
    for intent, n in intent_counts:
        if intent in INTENT_TX_KIND:
            counts[INTENT_TX_KIND[intent]] += n
    total = sum(counts.values())
    if total == 0:
        return {kind: float(kind == "plain") for kind in TX_KINDS}
    return {kind: counts[kind] / total for kind in TX_KINDS}


def engine_stats(root: str, actors: set[int] | None = None) -> Stats:
    """Statistics of a continuum export directory.

    actors, when given, restricts them to those actor ids (one cluster's
    actors, when grading per cluster).
    """
    con = _open_export(root, actors)
    stats = Stats()

    amounts = _column(con, AMOUNT_SQL)
    stats.samples["output_value_lovelace"] = amounts
    stats.samples["log10_output_value"] = np.log10(amounts) if len(amounts) else amounts

    span_ms = _first_value(con, SPAN_MS_SQL) or 0
    span_days = max(span_ms / MS_PER_DAY, _MIN_SPAN_DAYS)
    payments_per_actor = _column(con, PAYMENTS_PER_ACTOR_SQL)
    stats.samples["tx_count_per_address"] = payments_per_actor
    stats.samples["activity_per_day"] = payments_per_actor / span_days
    stats.info["span_days"] = span_days

    gaps = sorted_sample(row[0] / MS_PER_SECOND for row in con.execute(GAP_MS_SQL).fetchall())
    stats.samples["gap_seconds"] = gaps
    stats.samples["log10_gap_seconds"] = np.log10(np.maximum(gaps, MIN_GAP_SECONDS))

    stats.shares["tx_type_mix"] = type_mix(con.execute(SUBMITTED_INTENTS_SQL).fetchall())
    stats.fractions["address_reuse"] = float(_first_value(con, REUSE_SQL) or 0.0)
    stats.info["idle_fraction_of_records"] = idle_fraction(*con.execute(IDLE_SQL).fetchall()[0])
    stats.info["payments"] = float(len(amounts))
    return stats


# The network proposer's profile name (internal/engine, proposerName).
PROPOSER_PROFILE = "network"

# Each graded per-member rate of the actors block and the intent whose
# first attempts count its events. Registrations are births.
MEMBER_EVENTS = {
    "pools": {"update_rate": "update_pool", "retire_rate": "retire_pool"},
    "dreps": {"vote_rate": "vote", "retire_rate": "retire_drep"},
}

FIRST_ATTEMPTS_SQL = """
SELECT actor, kind, action, contract FROM submission WHERE attempt = 1
"""

SPAN_DAYS_SQL = 'SELECT max("at") FROM decision'

DEPLOYED_SQL = "SELECT count(*) FROM decision WHERE kind = 'Deployed'"


@dataclass
class ActorCounts:
    """What a run's non-wallet actors did, for the target's actors block.

    events maps "<population>.<rate>" (and "contracts.new_rate",
    "proposal_rate.<type>") to how many such events the run had; members
    is how many actors each population had, wallets the wallets the
    contract rate is per, voted the members of a population that voted at
    least once, top_share the share of script calls to the busiest 1% of
    contracts (None without script calls).
    """

    days: float
    members: dict[str, int] = field(default_factory=dict)
    wallets: int = 0
    events: dict[str, int] = field(default_factory=dict)
    voted: dict[str, int] = field(default_factory=dict)
    top_share: float | None = None


def _has_column(con: duckdb.DuckDBPyConnection, table: str, column: str) -> bool:
    return column in [row[0] for row in con.execute(f"DESCRIBE {table}").fetchall()]


def actor_counts(root: str, store: str | None, target_actors: dict) -> ActorCounts | None:
    """Count a run's pool, DRep, contract and proposal events.

    Populations are the run's actors whose profile is named like a
    population of the target (internal/engine names them so). An export
    from before the submission table had its kind column has nothing to
    count and gives None.
    """
    con = _open_export(root, None)
    if not _has_column(con, "submission", "kind"):
        return None
    profiles = actor_profiles(root, store)
    pops = [name for name in MEMBER_EVENTS if name in target_actors]
    counts = ActorCounts(days=(_first_value(con, SPAN_DAYS_SQL) or 0) / MS_PER_DAY)
    for name in pops:
        counts.members[name] = sum(1 for p in profiles.values() if p == name)
        counts.events[f"{name}.register_rate"] = _births(root, name)
    counts.wallets = sum(1 for p in profiles.values() if p not in pops and p != PROPOSER_PROFILE)

    by_kind: dict[tuple[str, str], set[int]] = {}
    calls: dict[int, int] = {}
    for actor, kind, action, contract in con.execute(FIRST_ATTEMPTS_SQL).fetchall():
        population = profiles.get(int(actor))
        for name in pops:
            for rate, intent in MEMBER_EVENTS[name].items():
                if population == name and kind == intent:
                    counts.events[f"{name}.{rate}"] = counts.events.get(f"{name}.{rate}", 0) + 1
                    by_kind.setdefault((name, intent), set()).add(int(actor))
        if kind == "propose":
            key = f"proposal_rate.{action or 'info'}"
            counts.events[key] = counts.events.get(key, 0) + 1
        if kind in ("lock", "unlock"):
            calls[int(contract or 0)] = calls.get(int(contract or 0), 0) + 1
    for name in pops:
        if "vote_rate" in MEMBER_EVENTS[name]:
            counts.voted[name] = len(by_kind.get((name, "vote"), set()))
        for rate in MEMBER_EVENTS[name]:
            counts.events.setdefault(f"{name}.{rate}", 0)
    counts.events["contracts.new_rate"] = int(_first_value(con, DEPLOYED_SQL) or 0)
    if calls:
        busiest = sorted(calls.values(), reverse=True)[:max(1, math.ceil(0.01 * len(calls)))]
        counts.top_share = sum(busiest) / sum(calls.values())
    return counts


def _births(root: str, profile: str) -> int:
    """Actors of a profile spawned during the run."""
    if not has_table(root, "actor_transition"):
        return 0
    return sum(1 for p in _born_actor_profiles(root).values() if p == profile)
