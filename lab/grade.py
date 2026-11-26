#!/usr/bin/env python3
"""Grade a devnet run against a fitted target.

    python grade.py EXPORT_DIR --target target.json --out report.md \
        [--chain-dir DEVNET_MITHRIL2PQ_DIR] [--slot-seconds 1]

EXPORT_DIR is `continuum export` output. KS statistics pass when the
two-sample KS distance D is at most --dmax (an effect-size bound: with
millions of samples the p-value rejects every real difference). The p-value
is reported alongside.
"""

from __future__ import annotations

import argparse
import json
import os
import sys

import numpy as np
from scipy import stats as sps

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common  # noqa: E402


def ks(sample: np.ndarray, target_quantiles: list[float], dmax: float) -> dict:
    """Two-sample KS of a sample against a target's quantile points."""
    if len(sample) == 0:
        return {"status": "skipped", "reason": "no samples"}
    r = sps.ks_2samp(sample, np.asarray(target_quantiles))
    return {
        "status": "pass" if r.statistic <= dmax else "fail",
        "D": float(r.statistic),
        "p": float(r.pvalue),
        "n": int(len(sample)),
        "median": float(np.median(sample)),
    }


def tv(a: dict, b: dict) -> float:
    return 0.5 * sum(abs(a.get(k, 0.0) - b.get(k, 0.0)) for k in set(a) | set(b))


def grade(st: common.Stats, target: dict, dmax: float, tvmax: float, fmax: float) -> list[dict]:
    rows = []
    for name in common.KS_STATS:
        r = ks(st.samples.get(name, np.array([])), target["quantiles"][name], dmax)
        r["name"] = name
        r["target_median"] = float(np.median(target["quantiles"][name]))
        rows.append(r)
    for name in common.SHARE_STATS:
        d = tv(st.shares[name], target["shares"][name])
        rows.append({"name": name, "status": "pass" if d <= tvmax else "fail", "TV": d,
                     "got": st.shares[name], "want": target["shares"][name]})
    for name in common.FRACTION_STATS:
        if name not in st.fractions:
            rows.append({"name": name, "status": "skipped", "reason": "needs chain tables"})
            continue
        d = abs(st.fractions[name] - target["fractions"][name])
        rows.append({"name": name, "status": "pass" if d <= fmax else "fail", "diff": d,
                     "got": st.fractions[name], "want": target["fractions"][name]})
    return rows


def report(rows: list[dict], st: common.Stats, args: argparse.Namespace) -> str:
    out = ["# Calibration grade", "",
           f"Run: `{args.export_dir}`; target: `{args.target}`; "
           f"KS bound D <= {args.dmax}, TV <= {args.tvmax}, fraction diff <= {args.fmax}.", "",
           "| statistic | verdict | detail |", "|---|---|---|"]
    for r in rows:
        if r["status"] == "skipped":
            d = r["reason"]
        elif "D" in r:
            d = f"D={r['D']:.3f} p={r['p']:.2g} n={r['n']} median {r['median']:.4g} vs target {r['target_median']:.4g}"
        elif "TV" in r:
            d = f"TV={r['TV']:.3f}; got {fmtmap(r['got'])}; want {fmtmap(r['want'])}"
        else:
            d = f"diff={r['diff']:.3f}; got {r['got']:.3f}, want {r['want']:.3f}"
        out.append(f"| {r['name']} | {r['status'].upper()} | {d} |")
    npass = sum(r["status"] == "pass" for r in rows)
    nfail = sum(r["status"] == "fail" for r in rows)
    out += ["", f"{npass} pass, {nfail} fail, {len(rows) - npass - nfail} skipped."]
    if "idle_fraction_of_records" in st.info:
        out += ["", f"Idle wake-ups (IntentChosen=idle plus Tick) are "
                f"{st.info['idle_fraction_of_records']:.1%} of decision records."]
    return "\n".join(out) + "\n"


def fmtmap(m: dict) -> str:
    return ", ".join(f"{k} {v:.3f}" for k, v in m.items())


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("export_dir")
    ap.add_argument("--target", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--chain-dir")
    ap.add_argument("--slot-seconds", type=float, default=1.0)
    ap.add_argument("--dmax", type=float, default=0.1)
    ap.add_argument("--tvmax", type=float, default=0.1)
    ap.add_argument("--fmax", type=float, default=0.1)
    a = ap.parse_args(argv)
    with open(a.target) as f:
        target = json.load(f)
    st = common.engine_stats(a.export_dir)
    if a.chain_dir:
        st = common.merge_chain(st, common.chain_stats(a.chain_dir, a.slot_seconds))
    rows = grade(st, target, a.dmax, a.tvmax, a.fmax)
    md = report(rows, st, a)
    with open(a.out, "w") as f:
        f.write(md)
    print(md)
    return 1 if any(r["status"] == "fail" for r in rows) else 0


if __name__ == "__main__":
    raise SystemExit(main())
