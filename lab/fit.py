#!/usr/bin/env python3
"""Fit profile distributions to chain data.

    python fit.py CHAIN_DIR --out OUT_DIR [--name mainnet-fit]

CHAIN_DIR is a mithril2pq output directory. Writes OUT_DIR/profile.yaml (a
profile file in internal/profile's syntax) and OUT_DIR/target.json (the
quantiles and shares grade.py tests a run against).
"""

from __future__ import annotations

import argparse
import json
import math
import os
import sys

import numpy as np
from scipy import stats as sps
from scipy.optimize import minimize

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common  # noqa: E402

TAIL_QUANTILE = 0.9
# profile.Pareto rejects an index at or below 1 for activity_tail.
MIN_TAIL = 1.05


def fit_lognormal(x: np.ndarray) -> tuple[float, float]:
    """MLE of the log-normal: mean and std of ln x."""
    x = x[x > 0]
    if len(x) < 2:
        return 0.0, 1.0
    lx = np.log(x)
    return float(lx.mean()), float(max(lx.std(), 1e-6))


def fit_pareto(x: np.ndarray, xm: float) -> float:
    """MLE of the Pareto index above xm: n / sum(ln(x / xm))."""
    t = x[x > xm]
    if len(t) < 2 or xm <= 0:
        return 2.0
    s = float(np.log(t / xm).sum())
    return float(len(t) / s) if s > 0 else 2.0


def fit_trunc_lognormal(x: np.ndarray, upper: float) -> tuple[float, float]:
    """MLE of a log-normal truncated above at upper: mean and std of ln x.

    A body cut at a quantile has a smaller spread than the log-normal it came
    from, so fitting the cut sample's moments understates sigma and mu; this
    maximises the truncated likelihood instead.
    """
    x = x[(x > 0) & (x <= upper)]
    mu0, sig0 = fit_lognormal(x)
    if len(x) < 50:
        return mu0, sig0
    y, b = np.log(x), math.log(upper)

    def nll(t: np.ndarray) -> float:
        mu, sig = t[0], math.exp(t[1])
        z = (y - mu) / sig
        return float(np.sum(math.log(sig) + 0.5 * z * z) + len(y) * sps.norm.logcdf((b - mu) / sig))

    # Mass piled on the bound (ties at a quantile) has no finite maximum: mu
    # and sigma run off together. The mean is capped at the bound and sigma at
    # a spread no payment distribution here approaches.
    bounds = [(float(y.min()) - 10.0, b), (math.log(1e-3), math.log(10.0))]
    start = [min(mu0, b), min(max(math.log(sig0), bounds[1][0]), bounds[1][1])]
    r = minimize(nll, start, method="Nelder-Mead", bounds=bounds,
                 options={"xatol": 1e-6, "fatol": 1e-9, "maxiter": 2000})
    if not np.isfinite(r.fun):
        return mu0, sig0
    return float(r.x[0]), float(math.exp(r.x[1]))


def fit_body_tail(x: np.ndarray, q: float = TAIL_QUANTILE) -> dict:
    """Log-normal body up to the q-quantile, Pareto tail above it.

    tail_share is the fraction of the sample strictly above xm, which is what
    the profile's amount.tail_share means. Payment sizes pile up on exact
    values (the 2 ADA minimum), so a quantile can sit on a tie; counting only
    the strictly larger values keeps that mass in the body, and the share is
    zero when nothing is larger.
    """
    xm = float(np.quantile(x, q))
    mu, sigma = fit_trunc_lognormal(x[x <= xm], xm)
    share = float((x > xm).mean())
    return {"xm": xm, "mu": mu, "sigma": sigma, "alpha": fit_pareto(x, xm), "q": q, "tail_share": share}


def fit_beta(p: np.ndarray) -> tuple[float, float]:
    """Method-of-moments beta fit; (1, 1) when the sample is degenerate."""
    p = p[np.isfinite(p)]
    if len(p) < 3:
        return 1.0, 1.0
    m, v = float(p.mean()), float(p.var())
    if v <= 1e-9 or not 0 < m < 1 or v >= m * (1 - m):
        return 1.0, 1.0
    k = m * (1 - m) / v - 1
    return float(m * k), float((1 - m) * k)


def fmt(v: float) -> str:
    return f"{v:.6g}"


# Activity is fitted per unit as a Pareto above a low quantile of the unit
# counts: the engine scales every actor's base rate by a Pareto(1, tail)
# multiplier, so the base rate is that quantile's rate and the tail index is
# the Pareto's.
ACTIVITY_FLOOR_Q = 0.1
MIN_BUDGET = 100_000_000
MAX_BUDGET = 1_000_000_000_000
# A budget this many times the tail scale lets a wallet make a typical large
# payment many times before the engine caps one to its balance.
BUDGET_TAILS = 20


MIN_ONSET = 1e-9
# internal/agenda/hodl.go scores a payment 0.02 against 1 for idling, and
# the engine samples an intent in proportion to score times the profile's
# weight, so a wake-up becomes a payment with probability 0.02 w / (1 + 0.02 w)
# for a send weight w. The base rate is the wake-up rate, so it is the payment
# rate divided by that probability.
HODL_SEND_SCORE = 0.02


def send_probability(weight: float) -> float:
    """Chance that a wake-up of a hodl actor with this send weight is a payment."""
    return HODL_SEND_SCORE * weight / (1.0 + HODL_SEND_SCORE * weight)


def fit_activity(st: common.Stats) -> dict:
    """Base send rate while active, tail index and active fraction.

    A unit averages counts/window transactions but only sends while active,
    so the rate while active is that average over the active fraction.
    """
    counts = st.samples["tx_count_per_address"]
    days = st.info["span_days"]
    f = max(st.info.get("active_fraction", 1.0), 1e-6)
    xm = max(float(np.quantile(counts, ACTIVITY_FLOOR_Q)), 1.0)
    tail = max(fit_pareto(counts[counts >= xm], xm), MIN_TAIL)
    return {"base_per_sec": xm / (f * days * 86400.0), "tail": tail, "active_fraction": f}


def dormancy_onset(active_fraction: float, mu: float, sigma: float) -> float:
    """Per-second rate of going dormant that leaves a unit active for the
    given fraction of the time: active spells of mean 1/onset alternate with
    dormancies of mean exp(mu + sigma^2/2)."""
    mean_dormant = math.exp(mu + sigma * sigma / 2)
    f = min(max(active_fraction, 1e-6), 1.0)
    return max((1.0 - f) / (f * mean_dormant), MIN_ONSET)


MIN_SAMPLES = 20


def fit_params(st: common.Stats, fallback: dict | None = None) -> dict:
    """Every fitted quantity of one population, as plain numbers.

    A piece the population has too few samples for (a tiny cluster has no
    gaps, say) takes the fallback's value, normally the overall fit; with no
    fallback it is fitted from whatever there is.
    """
    fb = fallback or {}

    out: dict = {}
    pay = st.samples["output_value_lovelace"]
    if len(pay) >= MIN_SAMPLES or (not fb and len(pay)):
        out["amount"] = fit_body_tail(st.samples["output_value_lovelace"])
    else:
        out["amount"] = fb["amount"]
    gaps = st.samples["gap_seconds"]
    gaps = gaps[gaps > 0]
    if len(gaps) >= MIN_SAMPLES or (not fb and len(gaps)):
        gap_xm = max(float(np.median(gaps)), 1.0)
        long = gaps[gaps > 86400]
        if len(long) < 30:
            long = gaps[gaps > gap_xm]
        dmu, dsig = fit_lognormal(long)
        out["gap"] = {"xm": gap_xm, "alpha": fit_pareto(gaps, gap_xm)}
        out["dormancy"] = {"mu": dmu, "sigma": dsig}
    else:
        out["gap"], out["dormancy"] = fb["gap"], fb["dormancy"]
    if len(st.samples["tx_count_per_address"]):
        out["activity"] = fit_activity(st)
    else:
        # The fallback's base rate is a wake-up rate: its payment rate already
        # divided by the fallback's send probability. Undo that, so the
        # division below applies this population's probability exactly once.
        out["activity"] = dict(fb["activity"])
        out["activity"]["base_per_sec"] *= fb["activity"].get("send_probability") or 1.0
    p_send = send_probability(mix_weights(st.shares["tx_type_mix"]).get("send", 0.0))
    out["activity"]["send_probability"] = p_send
    if p_send > 0:
        out["activity"]["base_per_sec"] /= p_send
    out["competence"] = fit_beta(st.per_address["change_to_sender"])
    out["care"] = fit_beta(st.per_address["address_reuse"])
    out["mix"] = st.shares["tx_type_mix"]
    out["budget"] = int(min(max(BUDGET_TAILS * out["amount"]["xm"], MIN_BUDGET), MAX_BUDGET))
    return out


def mix_weights(mix: dict[str, float]) -> dict[str, float]:
    """Intent weights from the type mix, rounded to sum to 1 within
    profile's tolerance (the last weight takes the remainder)."""
    weights = {"send": mix["plain"], "script_call": mix["script"],
               "delegate": mix["stake"], "register_pool": mix["pool"]}
    weights = {k: v for k, v in weights.items() if v > 0}
    z = sum(weights.values())
    weights = {k: v / z for k, v in weights.items()}
    keys = list(weights)
    rounded = {k: round(weights[k], 6) for k in keys}
    rounded[keys[-1]] = round(1.0 - sum(rounded[k] for k in keys[:-1]), 6)
    return rounded


def profile_lines(name: str, fp: dict, weight: float | None = None) -> list[str]:
    """One profile entry in internal/profile's syntax."""
    am, ac = fp["amount"], fp["activity"]
    comp, care = fp["competence"], fp["care"]
    w = mix_weights(fp["mix"])
    lines = [
        f"  - name: {name}",
        "    kind: wallet",
        "    agenda: hodl",
    ]
    if weight is not None:
        lines.append(f"    weight: {weight:.6f}")
    lines += [
        "    traits:",
        f"      competence: beta({fmt(comp[0])}, {fmt(comp[1])})",
        f"      patience: pareto(xm={fmt(fp['gap']['xm'])}, alpha={fmt(fp['gap']['alpha'])})",
        "      learning_rate: const(0)",
        f"      care: beta({fmt(care[0])}, {fmt(care[1])})",
        f"    budget: {fp['budget']}",
        "    amount:",
        f"      body: lognormal({fmt(am['mu'])}, {fmt(am['sigma'])})",
    ]
    if am["tail_share"] > 0:
        lines += [
            f"      tail: pareto(xm={fmt(am['xm'])}, alpha={fmt(am['alpha'])})",
            f"      tail_share: {fmt(am['tail_share'])}",
        ]
    lines += [
        "    rates:",
        "      base:",
        f"        send: {fmt(ac['base_per_sec'])}",
        f"      activity_tail: {fmt(ac['tail'])}",
        "    weights:",
    ]
    lines += [f"      {k}: {w[k]:.6f}" for k in w]
    lines += [
        "    hazards:",
        f"      dormancy: lognormal({fmt(fp['dormancy']['mu'])}, {fmt(fp['dormancy']['sigma'])})",
        f"      dormancy_onset: {fmt(dormancy_onset(ac['active_fraction'], fp['dormancy']['mu'], fp['dormancy']['sigma']))}",
    ]
    return lines


def target_of(st: common.Stats, fp: dict) -> dict:
    return {
        "info": st.info,
        "fit": fp,
        "quantiles": {k: common.quantiles(st.samples[k]) for k in common.KS_STATS if len(st.samples[k])},
        "shares": st.shares,
        "fractions": st.fractions,
    }


def build(st: common.Stats, name: str) -> tuple[str, dict]:
    """A one-profile file and its target from one population's statistics."""
    fp = fit_params(st)
    lines = [
        "# Fitted by lab/fit.py from chain data; see lab/README.md for the mapping.",
        "version: 1",
        "profiles:",
    ] + profile_lines(name, fp)
    return "\n".join(lines) + "\n", {"version": 1, **target_of(st, fp)}


def header_lines(h: dict) -> list[str]:
    """The comment header of a clustered profile file."""
    out = ["# Fitted by lab/fit.py --clusters; see lab/README.md. Do not edit by hand."]
    for k in ["network", "window", "seed", "k", "k_gmm", "k_bic_min", "k_interior", "bic", "unit", "weighting", "sampled_units", "units",
              "mithril2pq_version", "mithril2pq_last_chunk"]:
        if k in h:
            v = h[k]
            if isinstance(v, dict):
                v = ", ".join(f"{a}={b:.1f}" if isinstance(b, float) else f"{a}={b}" for a, b in v.items())
            out.append(f"# {k}: {v}")
    return out


def build_clusters(overall: common.Stats, clusters: dict[str, common.Stats], shares: dict[str, float],
                   header: dict) -> tuple[str, dict]:
    """A clustered profile file and its target.

    clusters maps each cluster name to its statistics; shares gives each one's
    weight (they sum to 1). The target carries the overall statistics and each
    cluster's, so grade.py can grade a run as a whole and per cluster.
    """
    lines = header_lines(header) + ["version: 1", "profiles:"]
    ofp = fit_params(overall)
    target = {"version": 1, "header": header, "overall": target_of(overall, ofp), "clusters": {}}
    for name in sorted(clusters):
        fp = fit_params(clusters[name], ofp)
        lines += profile_lines(name, fp, shares[name])
        t = target_of(clusters[name], fp)
        t["share"] = shares[name]
        target["clusters"][name] = t
    return "\n".join(lines) + "\n", target


def fit_clusters(root: str, a: argparse.Namespace) -> tuple[str, dict]:
    """Cluster the units active in a window of the chain and fit each cluster."""
    import cluster
    import pandas as pd
    import units

    last = a.window_end_slot or common.last_slot(root)
    window_slots = int(a.window_days * 86400 / a.slot_seconds)
    first = last - window_slots
    con = common.prepare(root, first, last, units.unit_of(a.unit == "stake"), a.memory_limit)
    df = units.features(con, window_slots, a.slot_seconds)
    res = cluster.cluster_units(df, a.kmin, a.kmax, a.seed, a.max_units, a.select)
    names, labels = res.names, res.labels
    df["grp"] = [names[i] for i in labels]
    con.register("c_group_df", df[["unit", "grp"]])
    con.execute("CREATE OR REPLACE TEMP TABLE c_group AS SELECT * FROM c_group_df")
    span = window_slots * a.slot_seconds
    stats = {}
    for n in names:
        common.scope(con, n)
        stats[n] = common.stats_from(con, a.slot_seconds, span)
    common.scope(con)
    overall = common.stats_from(con, a.slot_seconds, span)
    if a.weighting == "transactions":
        raw = {n: float(df.loc[df.grp == n, "n_tx"].sum()) for n in names}
    else:
        raw = {n: float((df.grp == n).sum()) for n in names}
    total = sum(raw.values())
    shares = {n: raw[n] / total for n in names}
    meta = chain_meta(root)
    header = {
        "network": a.network,
        "window": f"{a.window_days} days, slots {first}..{last}",
        "window_days": a.window_days,
        "window_first_slot": first,
        "window_last_slot": last,
        "seed": a.seed,
        "k": res.k,
        "k_gmm": res.fit.k,
        "k_bic_min": res.fit.k_min,
        "k_interior": res.fit.interior,
        "bic": {str(k): v for k, v in res.fit.bic.items()},
        "unit": "stake credential (address when none)" if a.unit == "stake" else "address",
        "weighting": a.weighting,
        "units": len(df),
        "sampled_units": min(int((cluster.segment(df) == cluster.GENERAL).sum()), a.max_units),
        "mithril2pq_version": a.m2pq_version or meta.get("version", "unknown"),
        "mithril2pq_last_chunk": meta.get("last_chunk", "unknown"),
    }
    return build_clusters(overall, stats, shares, header)


def chain_meta(root: str) -> dict:
    """The mithril2pq manifest of a chain directory, or {} when absent."""
    try:
        with open(os.path.join(root, "_mithril2pq.json")) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("chain_dir")
    ap.add_argument("--out", required=True)
    ap.add_argument("--name", default="chain-fit")
    ap.add_argument("--slot-seconds", type=float, default=1.0)
    ap.add_argument("--clusters", action="store_true", help="cluster units and write NETWORK.yaml")
    ap.add_argument("--network", default="chain")
    ap.add_argument("--window-days", type=float, default=90.0)
    ap.add_argument("--window-end-slot", type=int, default=0, help="default: the newest block with a transaction")
    ap.add_argument("--seed", type=int, default=1)
    ap.add_argument("--unit", choices=["stake", "address"], default="stake")
    ap.add_argument("--weighting", choices=["addresses", "transactions"], default="addresses")
    ap.add_argument("--kmin", type=int, default=2)
    ap.add_argument("--kmax", type=int, default=15)
    ap.add_argument("--select", choices=["elbow", "min"], default="elbow", help="k from the BIC elbow or its minimum")
    ap.add_argument("--max-units", type=int, default=100_000, help="units sampled for the mixture fit")
    ap.add_argument("--memory-limit", default=None, help="DuckDB memory limit, e.g. 8GB")
    ap.add_argument("--m2pq-version", default=None)
    a = ap.parse_args(argv)
    os.makedirs(a.out, exist_ok=True)
    if a.clusters:
        yml, target = fit_clusters(a.chain_dir, a)
        yml_name, tgt_name = f"{a.network}.yaml", f"{a.network}.target.json"
        msg = f"k={target['header']['k']}, {target['header']['units']} units"
    else:
        st = common.chain_stats(a.chain_dir, a.slot_seconds)
        yml, target = build(st, a.name)
        yml_name, tgt_name = "profile.yaml", "target.json"
        msg = f"{int(st.info['txs'])} txs"
    with open(os.path.join(a.out, yml_name), "w") as f:
        f.write(yml)
    with open(os.path.join(a.out, tgt_name), "w") as f:
        json.dump(target, f, separators=(",", ":"))
    print(f"wrote {a.out}/{yml_name} and {tgt_name} ({msg})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
