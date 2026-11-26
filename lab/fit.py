#!/usr/bin/env python3
"""Fit profile distributions to chain data.

    python fit.py CHAIN_DIR --out OUT_DIR [--name mainnet-fit]
    python fit.py CHAIN_DIR --out OUT_DIR --clusters --network preprod

CHAIN_DIR is a mithril2pq output directory. Without --clusters this writes
OUT_DIR/profile.yaml (one profile for everyone, in internal/profile's
syntax) and OUT_DIR/target.json (the quantiles and shares grade.py tests a
run against). With --clusters it writes OUT_DIR/NETWORK.yaml with one
profile per behavioural cluster and OUT_DIR/NETWORK.target.json.

This is the first step of the calibration loop (guide.ipynb, section 6):

    fit    measure a real chain, fit a profile             (this file)
    run    continuum run --profiles ... ; continuum export
    grade  measure the run the same way, compare           (grade.py)

and the fit itself reads top to bottom as

    measure   chain_stats      SQL -> named statistics
    cluster   unit_features, cluster     (only with --clusters)
    fit       population_fit   statistics -> distribution parameters
    write     profile_yaml     parameters -> profile.yaml + target.json

With --clusters the non-wallet populations are fitted too:

    measure   actor_stats      pool, DRep, vote, proposal and contract rows
    fit       actor_fit        head counts (cut to one machine), rates
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from contextlib import contextmanager
from dataclasses import dataclass

import pandas as pd

import actor_fit
import actor_stats
import chain_stats
import cluster
import units
from cardano import DEFAULT_SLOT_SECONDS, SECONDS_PER_DAY, slots_in_days
from population_fit import fit_population
from population_stats import Stats
from profile_yaml import render_clustered, render_single
from unit_features import unit_features

# How far back from the newest block the clustered fit looks. The window
# must be long enough to see a quiet unit transact more than once (gaps
# need two transactions) and short enough that behaviour is current and a
# unit has not changed habits half way. 90 days sees a monthly user about
# three times. A shorter window turns occasional payers into one-off
# holders and cuts off dormancies longer than itself; a longer one costs
# memory and mixes eras. Override with --window-days.
DEFAULT_WINDOW_DAYS = 90.0

# Search range for the number of mixture components. Two is the smallest
# mixture. The upper end is a budget, not a finding: on the shipped
# networks BIC is still falling at 15 (k_bic_min is 15 and k_interior is
# False in their headers), which is why the elbow and not the minimum
# chooses k. Raising it costs fitting time and does not move the elbow
# unless the curve steepens again.
DEFAULT_KMIN, DEFAULT_KMAX = 2, 15

# At most this many units feed the mixture fit; all units are then assigned.
DEFAULT_MAX_UNITS = 100_000

MANIFEST_FILE = "_mithril2pq.json"


def fit_profile(stats: Stats, name: str) -> tuple[str, dict]:
    """A one-profile file and its target from one population's statistics."""
    return render_single(name, stats, fit_population(stats))


def fit_cluster_profiles(overall: Stats, clusters: dict[str, Stats], shares: dict[str, float],
                         header: dict, actors: actor_fit.ActorFit | None = None) -> tuple[str, dict]:
    """A clustered profile file and its target.

    clusters maps each cluster name to its statistics; shares gives each
    one's weight (they sum to 1). The whole population is fitted first, and
    a cluster too small to fit some piece on its own borrows that piece
    from the overall fit. The agenda each cluster got is added to header.
    """
    overall_fit = fit_population(overall)
    fitted = {name: (stats, fit_population(stats, overall_fit)) for name, stats in clusters.items()}
    header["agendas"] = ", ".join(f"{name}={fitted[name][1].agenda}" for name in sorted(fitted))
    if actors is not None:
        header.update(actor_header(actors))
    return render_clustered((overall, overall_fit), fitted, shares, header, actors)


def actor_header(actors: actor_fit.ActorFit) -> dict:
    """The head counts of the non-wallet populations and how far they were
    cut for one machine."""
    out = {}
    for key, population in (("pools", actors.pools), ("dreps", actors.dreps)):
        if population is not None:
            out[key] = f"{population.count} of {population.chain_count} (scale {population.scale:.6g})"
    if actors.contracts is not None:
        out["contracts"] = f"{actors.contracts.chain_count} used in the window, not run as actors (scale 1)"
    return out


@dataclass(frozen=True)
class Window:
    """The slots a clustered fit observes: [first_slot, last_slot]."""

    first_slot: int
    last_slot: int
    slots: int
    slot_seconds: float

    @property
    def seconds(self) -> float:
        return self.slots * self.slot_seconds


def window_ending_at(last_slot: int, days: float, slot_seconds: float) -> Window:
    """The window of the given length that ends at last_slot."""
    slots = slots_in_days(days, slot_seconds)
    return Window(first_slot=last_slot - slots, last_slot=last_slot, slots=slots, slot_seconds=slot_seconds)


def cluster_shares(units_table: pd.DataFrame, cluster_of_unit: pd.Series, names: list[str],
                   weighting: str) -> dict[str, float]:
    """Each cluster's weight in the profile file; the weights sum to 1.

    weighting="addresses" counts units: a devnet seeded this way has a
    realistic *population* (mostly quiet holders). weighting="transactions"
    counts the transactions they sent: a realistic *traffic* mix, dominated
    by the few busy units. The two differ because activity is heavy-tailed.
    """
    if weighting == "transactions":
        size = {name: float(units_table.loc[cluster_of_unit == name, "tx_count"].sum()) for name in names}
    else:
        size = {name: float((cluster_of_unit == name).sum()) for name in names}
    total = sum(size.values())
    return {name: size[name] / total for name in names}


def measure_clusters(con, names: list[str], window: Window) -> tuple[Stats, dict[str, Stats]]:
    """Statistics of the whole window and of each cluster's transactions."""
    by_cluster = {}
    for name in names:
        chain_stats.scope_to_group(con, name)
        by_cluster[name] = chain_stats.measure(con, window.slot_seconds, window.seconds)
    chain_stats.scope_to_group(con)
    overall = chain_stats.measure(con, window.slot_seconds, window.seconds)
    return overall, by_cluster


def read_manifest(chain_dir: str) -> dict:
    """The mithril2pq manifest of a chain directory, or {} when it is absent
    or unreadable. It is only quoted in the header as provenance, so a fit
    of a directory without one still succeeds."""
    try:
        with open(os.path.join(chain_dir, MANIFEST_FILE)) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def fit_header(args: argparse.Namespace, window: Window, clustering: cluster.Clustering, unit_count: int,
               manifest: dict) -> dict:
    """Everything needed to repeat a clustered fit and to judge it."""
    return {
        "network": args.network,
        "window": f"{args.window_days} days, slots {window.first_slot}..{window.last_slot}",
        "window_days": args.window_days,
        "window_first_slot": window.first_slot,
        "window_last_slot": window.last_slot,
        "seed": args.seed,
        "k": clustering.k,
        "k_gmm": clustering.fit.k,
        "k_bic_min": clustering.fit.k_min,
        "k_interior": clustering.fit.interior,
        "bic": {str(k): bic for k, bic in clustering.fit.bic.items()},
        "unit": "stake credential (address when none)" if args.unit == "stake" else "address",
        "weighting": args.weighting,
        "units": unit_count,
        "sampled_units": clustering.sampled_units,
        "sampling": (f"mixture fitted on a seeded sample of {clustering.sampled_units} of "
                     f"{clustering.general_units} general units (cap --max-units {args.max_units}); "
                     f"all {unit_count} units assigned"),
        "mithril2pq_version": args.m2pq_version or manifest.get("version", "unknown"),
        "mithril2pq_last_chunk": manifest.get("last_chunk", "unknown"),
    }


@contextmanager
def timed(step: str):
    """Print how long one step of a fit took, so a slow network shows
    where its time goes."""
    start = time.monotonic()
    yield
    print(f"[fit] {step}: {time.monotonic() - start:.1f}s", file=sys.stderr, flush=True)


def fit_clustered_chain(chain_dir: str, args: argparse.Namespace) -> tuple[str, dict, pd.DataFrame]:
    """Cluster the units active in a window of the chain and fit each cluster.

    Returns the profile text, the target and every unit's scatter
    coordinates with its cluster (for export_guide_data.py)."""
    last_slot = args.window_end_slot or chain_stats.last_slot(chain_dir)
    window = window_ending_at(last_slot, args.window_days, args.slot_seconds)
    manifest = read_manifest(chain_dir)
    with timed("load window"):
        con = chain_stats.load_window(
            chain_dir, window.first_slot, window.last_slot, units.unit_of(args.unit == "stake"), args.memory_limit)
    with timed("unit features"):
        units_table = unit_features(con, window.slots, window.slot_seconds)
    with timed("cluster"):
        clustering = cluster.cluster_units(units_table, args.kmin, args.kmax, args.seed, args.max_units, args.select)
        cluster_of_unit = pd.Series([clustering.names[label] for label in clustering.labels])
        chain_stats.set_unit_groups(con, list(units_table["unit"]), list(cluster_of_unit))
    with timed("measure clusters"):
        overall, by_cluster = measure_clusters(con, clustering.names, window)
    shares = cluster_shares(units_table, cluster_of_unit, clustering.names, args.weighting)
    header = fit_header(args, window, clustering, len(units_table), manifest)
    with timed("non-wallet populations"):
        events = actor_stats.actor_events(con, chain_dir, window.first_slot, window.last_slot, manifest)
        actors = actor_fit.fit_actors(events, window.first_slot, window.last_slot,
                                      window.seconds / SECONDS_PER_DAY, len(units_table), args.network)
    with timed("fit and render"):
        text, target = fit_cluster_profiles(overall, by_cluster, shares, header, actors)
    unit_clusters = units_table[["unit", "tx_per_day", "median_payment_lovelace"]].assign(cluster=cluster_of_unit)
    return text, target, unit_clusters


def write_fit(out_dir: str, profile_name: str, profile_text: str, target_name: str, target: dict) -> None:
    """Write the profile file and its target into out_dir."""
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, profile_name), "w") as f:
        f.write(profile_text)
    with open(os.path.join(out_dir, target_name), "w") as f:
        json.dump(target, f, separators=(",", ":"))


def parse_args(argv: list[str] | None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("chain_dir")
    ap.add_argument("--out", required=True)
    ap.add_argument("--name", default="chain-fit")
    ap.add_argument("--slot-seconds", type=float, default=DEFAULT_SLOT_SECONDS)
    ap.add_argument("--clusters", action="store_true", help="cluster units and write NETWORK.yaml")
    ap.add_argument("--network", default="chain")
    ap.add_argument("--window-days", type=float, default=DEFAULT_WINDOW_DAYS)
    ap.add_argument("--window-end-slot", type=int, default=0, help="default: the newest block with a transaction")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--unit", choices=["stake", "address"], default="stake")
    ap.add_argument("--weighting", choices=["addresses", "transactions"], default="addresses")
    ap.add_argument("--kmin", type=int, default=DEFAULT_KMIN)
    ap.add_argument("--kmax", type=int, default=DEFAULT_KMAX)
    ap.add_argument("--select", choices=["elbow", "min"], default="elbow", help="k from the BIC elbow or its minimum")
    ap.add_argument("--max-units", type=int, default=DEFAULT_MAX_UNITS, help="units sampled for the mixture fit")
    ap.add_argument("--memory-limit", default=None, help="DuckDB memory limit, e.g. 8GB")
    ap.add_argument("--m2pq-version", default=None)
    ap.add_argument("--units-out", default=None, help="with --clusters: write each unit's cluster here (Parquet)")
    return ap.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    if args.clusters:
        profile_text, target, unit_clusters = fit_clustered_chain(args.chain_dir, args)
        if args.units_out:
            os.makedirs(os.path.dirname(os.path.abspath(args.units_out)), exist_ok=True)
            unit_clusters.to_parquet(args.units_out, index=False)
        profile_name, target_name = f"{args.network}.yaml", f"{args.network}.target.json"
        summary = f"k={target['header']['k']}, {target['header']['units']} units"
    else:
        stats = chain_stats.chain_stats(args.chain_dir, args.slot_seconds)
        profile_text, target = fit_profile(stats, args.name)
        profile_name, target_name = "profile.yaml", "target.json"
        summary = f"{int(stats.info['txs'])} txs"
    write_fit(args.out, profile_name, profile_text, target_name, target)
    print(f"wrote {args.out}/{profile_name} and {target_name} ({summary})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
