#!/usr/bin/env python3
"""Grade a devnet run against a fitted target.

    python grade.py EXPORT_DIR [--target target.json] --out report.md \
        [--store RUN/store.db] [--profiles-dir DIR] \
        [--chain-dir DEVNET_MITHRIL2PQ_DIR] [--slot-seconds 1]

EXPORT_DIR is `continuum export` output. Without --target the target comes
from the run's profile hash (the export's run table) through
PROFILES_DIR/index.json, which `continuum profiles index` writes. A clustered
target is graded as a whole and per cluster; a cluster's actors come from the
run's store.db (default: ../store.db beside the export). KS statistics pass when the
two-sample KS distance D is at most --dmax (an effect-size bound: with
millions of samples the p-value rejects every real difference). The p-value
is reported alongside.

This is the last step of the calibration loop (guide.ipynb, section 6):

    fit    measure a real chain, fit a profile             (fit.py)
    run    continuum run --profiles ... ; continuum export
    grade  measure the run the same way, compare           (this file)

and grading reads top to bottom as

    load the target      what the chain looked like (target.json)
    measure the run      engine_stats, the same named statistics
    compare              compare.py: KS distance, total variation, difference
    report               report.py: one Markdown table per population

The exit code is 1 when any statistic fails, so a script can gate on it.
"""

from __future__ import annotations

import argparse
import json
import os

import chain_stats
import engine_stats
import report
from compare import (FRACTION_PASS_DISTANCE, KS_PASS_DISTANCE, SHARE_PASS_DISTANCE, Bounds, Skipped,
                     any_failed, compare_population)
from population_stats import with_chain_tables

INDEX_FILE = "index.json"
STORE_FILE = "store.db"


def default_profiles_dir() -> str:
    """The shipped profiles: CONTINUUM_PROFILES, else ../profiles of this file."""
    here = os.path.dirname(os.path.abspath(__file__))
    return os.environ.get("CONTINUUM_PROFILES", os.path.join(here, "..", "profiles"))


def find_target(export_dir: str, profiles_dir: str) -> str:
    """Path of the target file for a run, by the profile hash in its header.

    A run records the hash of the profile file it used. index.json maps
    each shipped profile's hash to its target, so a run is always graded
    against the fit it was actually given, never against a stale one.
    """
    profile_hash = engine_stats.run_profile_hash(export_dir)
    index_path = os.path.join(profiles_dir, INDEX_FILE)
    try:
        with open(index_path) as f:
            index = json.load(f)
    except OSError as e:
        raise SystemExit(f"grade: cannot read {index_path} ({e}); pass --target") from e
    if profile_hash not in index:
        raise SystemExit(
            f"grade: run profile hash {profile_hash[:16]} is not in {index_path}; the run used profiles with no "
            "shipped target (or an edited copy): pass --target"
        )
    return os.path.join(profiles_dir, index[profile_hash]["target"])


def target_views(target: dict) -> tuple[dict, dict[str, dict]]:
    """The overall target and the per-cluster targets of a target file.

    A one-profile target is its own overall view with no clusters.
    """
    if "clusters" in target:
        return target["overall"], target["clusters"]
    return target, {}


def grade_clusters(export_dir: str, store: str | None, clusters: dict[str, dict],
                   bounds: Bounds) -> dict[str, report.ClusterGrade]:
    """Grade each cluster against the run's actors that used its profile.

    A cluster with no actors, or whose actors made no payment, has nothing
    to compare and gets a single skipped row.
    """
    profile_of_actor = engine_stats.actor_profiles(export_dir, store)
    grades = {}
    for name, cluster_target in clusters.items():
        actors = {actor for actor, profile in profile_of_actor.items() if profile == name}
        stats = engine_stats.engine_stats(export_dir, actors)
        if not actors or len(stats.samples["output_value_lovelace"]) == 0:
            rows = [Skipped("(cluster)", f"{len(actors)} actors, no payments in the run")]
        else:
            rows = compare_population(stats, cluster_target, bounds)
        grades[name] = report.ClusterGrade(rows, cluster_target.get("share"), len(actors))
    return grades


def parse_args(argv: list[str] | None) -> argparse.Namespace:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("export_dir")
    ap.add_argument("--target")
    ap.add_argument("--store")
    ap.add_argument("--profiles-dir", default=default_profiles_dir())
    ap.add_argument("--out", required=True)
    ap.add_argument("--chain-dir")
    ap.add_argument("--slot-seconds", type=float, default=1.0)
    ap.add_argument("--dmax", type=float, default=KS_PASS_DISTANCE)
    ap.add_argument("--tvmax", type=float, default=SHARE_PASS_DISTANCE)
    ap.add_argument("--fmax", type=float, default=FRACTION_PASS_DISTANCE)
    return ap.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    bounds = Bounds(ks=args.dmax, share=args.tvmax, fraction=args.fmax)

    target_path = args.target or find_target(args.export_dir, args.profiles_dir)
    with open(target_path) as f:
        overall_target, cluster_targets = target_views(json.load(f))

    run = engine_stats.engine_stats(args.export_dir)
    if args.chain_dir:
        run = with_chain_tables(run, chain_stats.chain_stats(args.chain_dir, args.slot_seconds))

    overall = compare_population(run, overall_target, bounds)
    clusters = {}
    if cluster_targets:
        store = args.store or os.path.join(os.path.dirname(os.path.abspath(args.export_dir)), STORE_FILE)
        clusters = grade_clusters(args.export_dir, store, cluster_targets, bounds)

    text = report.render(args.export_dir, target_path, bounds, overall,
                         run.info.get("idle_fraction_of_records"), clusters)
    with open(args.out, "w") as f:
        f.write(text)
    print(text)
    every_row = overall + [row for grade in clusters.values() for row in grade.rows]
    return 1 if any_failed(every_row) else 0


if __name__ == "__main__":
    raise SystemExit(main())
