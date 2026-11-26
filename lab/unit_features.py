"""One row of behaviour per unit: the table that clustering works on.

I/O: reads the tables chain_stats.load_window built and returns a pandas
DataFrame. cluster.py takes it from there.

Guide: section 7 of guide.ipynb ("Features per unit").
"""

from __future__ import annotations

import duckdb
import pandas as pd

from cardano import SECONDS_PER_DAY
from population_stats import Quantity

# The columns of the feature table, in order. One row per unit that sent at
# least one transaction in the window.
COLUMNS = (
    Quantity(
        "unit", "unit key",
        "The unit (units.py): a stake credential, or an address without one.",
        "Identifies the row; joins a unit back to its transactions.",
    ),
    Quantity(
        "tx_count", "transactions",
        "Transactions the unit sent in the window.",
        "Weights a cluster by traffic instead of by head count.",
    ),
    Quantity(
        "tx_per_day", "transactions per day",
        "tx_count divided by the window length in days.",
        "How busy the unit is; the main axis separating holders from traders.",
    ),
    Quantity(
        "median_gap_seconds", "seconds",
        "Median time between consecutive transactions. A unit with a single "
        "transaction has no gap and gets the window length.",
        "Typical rhythm: bursts of activity give a short median even when "
        "the unit is rarely active.",
    ),
    Quantity(
        "median_payment_lovelace", "lovelace",
        "Median size of the unit's payments, change excluded; of all its "
        "outputs when it only ever paid itself; 1 when it has none.",
        "Separates small retail payments from large transfers.",
    ),
    Quantity(
        "script_share", "share of transactions",
        "Share of the unit's transactions that ran a script.",
        "Marks dApp users and bots; used to segment, not to cluster.",
    ),
    Quantity(
        "stake_share", "share of transactions",
        "Share whose kind is 'stake': a stake certificate (registration, "
        "delegation) with no script and no pool certificate.",
        "Marks units that exist to stake; used to segment, not to cluster.",
    ),
    Quantity(
        "gov_share", "share of transactions",
        "Share whose kind is 'gov': a vote, a proposal, a DRep certificate or "
        "a vote delegation, with no script and no pool certificate.",
        "Marks units that take part in governance; used to segment, not to cluster.",
    ),
    Quantity(
        "inputs_per_tx", "count",
        "Average spent inputs per transaction.",
        "Many inputs mean a wallet full of small outputs (collecting).",
    ),
    Quantity(
        "outputs_per_tx", "count",
        "Average outputs per transaction.",
        "Many outputs mean batch payouts (distributing).",
    ),
    Quantity(
        "reuse_share", "share of outputs",
        "Share of the outputs of the unit's transactions that pay an address "
        "already paid earlier in the window.",
        "A privacy habit. Not a clustering feature; fitted per cluster as "
        "the care trait.",
    ),
    Quantity(
        "active_days", "days",
        "Distinct days on which the unit sent a transaction.",
        "Tells a unit that is steadily present from one that did everything "
        "in one sitting.",
    ),
    Quantity(
        "longest_gap_seconds", "seconds",
        "Longest time between consecutive transactions; 0 for a single "
        "transaction.",
        "How long the unit can go quiet: its dormancy.",
    ),
)

FEATURE_SQL = """
WITH
-- The unit's transactions. Transactions with an unknown sender have no unit.
unit_tx AS (
  SELECT unit, hash, slot, block_index, n_in, n_out, kind
  FROM tx_all WHERE unit IS NOT NULL),
-- Slots between each transaction and the unit's previous one. The first
-- transaction of a unit has no previous one: its gap is NULL.
tx_gap AS (
  SELECT unit, slot - lag(slot) OVER (PARTITION BY unit ORDER BY slot, block_index) AS gap
  FROM unit_tx),
-- Typical and longest gap per unit, in slots. Units with one transaction
-- have no row here.
gap_summary AS (
  SELECT unit, median(gap) AS median_gap, max(gap) AS longest_gap
  FROM tx_gap WHERE gap IS NOT NULL GROUP BY unit),
-- Counts and averages over the unit's transactions. A day is 86400 seconds
-- of slot time, so the slot length converts a slot to its day number.
tx_summary AS (
  SELECT unit, count(*) AS tx_count,
         count(DISTINCT floor(slot * {slot_seconds} / 86400)) AS active_days,
         avg(n_in) AS inputs_per_tx, avg(n_out) AS outputs_per_tx,
         avg((kind = 'script')::int) AS script_share, avg((kind = 'stake')::int) AS stake_share,
         avg((kind = 'gov')::int) AS gov_share
  FROM unit_tx GROUP BY unit),
-- Median output value per unit: over payments (outputs that leave the
-- unit, so change is excluded) and over all outputs as a fallback.
payment_summary AS (
  SELECT f.unit, median(o.lovelace) FILTER (WHERE o.paid_unit <> f.unit) AS median_payment,
         median(o.lovelace) AS median_output
  FROM unit_tx f JOIN paid_output o ON o.hash = f.hash WHERE o.lovelace > 0 GROUP BY f.unit),
-- Share of the unit's outputs that pay an already-used address.
reuse_summary AS (
  SELECT f.unit, avg(o.is_reuse::int) AS reuse_share
  FROM unit_tx f JOIN paid_output o ON o.hash = f.hash GROUP BY f.unit)
-- One row per unit, with the defaults for units that lack a summary.
SELECT b.unit, b.tx_count, b.active_days, b.inputs_per_tx, b.outputs_per_tx, b.script_share, b.stake_share, b.gov_share,
       coalesce(gs.median_gap, {window_slots}) AS median_gap_slots,
       coalesce(gs.longest_gap, 0) AS longest_gap_slots,
       coalesce(v.median_payment, v.median_output, 1) AS median_payment_lovelace,
       coalesce(r.reuse_share, 0) AS reuse_share
FROM tx_summary b
LEFT JOIN gap_summary gs USING (unit)
LEFT JOIN payment_summary v USING (unit)
LEFT JOIN reuse_summary r USING (unit)
ORDER BY b.unit
"""


def unit_features(con: duckdb.DuckDBPyConnection, window_slots: int, slot_seconds: float = 1.0) -> pd.DataFrame:
    """One row per unit with the columns documented in COLUMNS.

    con comes from chain_stats.load_window. window_slots is the length of
    the loaded window; slot_seconds converts slots to seconds (1 on mainnet,
    preprod and preview since Shelley).
    """
    table = con.execute(
        FEATURE_SQL.format(window_slots=int(window_slots), slot_seconds=float(slot_seconds))
    ).fetchdf()
    return in_time_units(table, window_slots, slot_seconds)


def in_time_units(table: pd.DataFrame, window_slots: int, slot_seconds: float) -> pd.DataFrame:
    """Convert the slot-based columns of the SQL result to seconds and days,
    and put the columns in the documented order."""
    window_days = window_slots * slot_seconds / float(SECONDS_PER_DAY)
    table["tx_per_day"] = table["tx_count"] / window_days
    table["median_gap_seconds"] = table["median_gap_slots"].astype(float) * slot_seconds
    table["longest_gap_seconds"] = table["longest_gap_slots"].astype(float) * slot_seconds
    table["median_payment_lovelace"] = table["median_payment_lovelace"].astype(float)
    return table[[column.name for column in COLUMNS]]
