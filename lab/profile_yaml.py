"""Write fitted populations as a profile file and as a grading target.

Pure text and dict building: nothing here touches a file. fit.py writes
what these functions return.

Two outputs describe the same fit for two readers:

  * the **profile** (YAML) is what the engine runs: distributions written
    as `lognormal(mu, sigma)`, `pareto(xm=..., alpha=...)`, `beta(a, b)`,
    in the syntax internal/profile parses;
  * the **target** (JSON) is what a run is graded against: the measured
    quantiles, shares and fractions, plus the fitted numbers for plots.

Guide: section 6 ("Calibration") and section 8 ("What each network looks
like") of guide.ipynb.
"""

from __future__ import annotations

from actor_fit import ActorFit, SpikeFit
from agendas import WEIGHTED
from population_fit import PopulationFit, dormancy_onset
from population_stats import KS_STATS, Stats, quantiles

PROFILE_VERSION = 1

# Header keys of a clustered profile file, in the order they are written.
HEADER_KEYS = [
    "network", "window", "seed", "k", "k_gmm", "k_bic_min", "k_interior", "bic", "unit", "weighting",
    "sampled_units", "units", "sampling", "agendas", "pools", "dreps", "contracts",
    "mithril2pq_version", "mithril2pq_last_chunk",
]


def number(value: float) -> str:
    """A number as the profile file shows it: six significant digits.

    Six digits is far finer than any fit is accurate, and it keeps the file
    stable when a refit changes only the last bits of a float.
    """
    return f"{value:.6g}"


def _beta_text(shape: tuple[float, float]) -> str:
    return f"beta({number(shape[0])}, {number(shape[1])})"


def _amount_lines(fit: PopulationFit) -> list[str]:
    """The amount block. The tail is left out when no payment exceeds the
    body's upper end, because a tail with share zero is never drawn."""
    amount = fit.amount
    lines = [
        "    amount:",
        f"      body: lognormal({number(amount.mu)}, {number(amount.sigma)})",
    ]
    if amount.tail_share > 0:
        lines += [
            f"      tail: pareto(xm={number(amount.xm)}, alpha={number(amount.alpha)})",
            f"      tail_share: {number(amount.tail_share)}",
        ]
    return lines


def profile_lines(name: str, fit: PopulationFit, weight: float | None = None) -> list[str]:
    """One profile entry in internal/profile's syntax.

    weight is the cluster's share of the population; a single-profile file
    has none.
    """
    activity, dormancy = fit.activity, fit.dormancy
    onset = dormancy_onset(activity.active_fraction, dormancy.mu, dormancy.sigma)
    lines = [
        f"  - name: {name}",
        "    kind: wallet",
        f"    agenda: {fit.agenda}",
    ]
    if weight is not None:
        lines.append(f"    weight: {weight:.6f}")
    lines += [
        "    traits:",
        f"      competence: {_beta_text(fit.competence)}",
        f"      patience: pareto(xm={number(fit.gap.xm)}, alpha={number(fit.gap.alpha)})",
        "      learning_rate: const(0)",
        f"      care: {_beta_text(fit.care)}",
        f"    budget: {fit.budget}",
    ]
    lines += _amount_lines(fit)
    lines += [
        "    rates:",
        "      base:",
        # The engine wakes an actor at the sum of its base rates and lets the
        # agenda pick the intent, so one rate carries the whole wake-up rate;
        # it is keyed by the agenda's main intent for the reader.
        f"        {WEIGHTED[fit.agenda][0]}: {number(activity.base_per_sec)}",
        f"      activity_tail: {number(activity.tail)}",
    ]
    if fit.hawkes is not None:
        lines.append(f"      hawkes: {{alpha: {number(fit.hawkes.alpha)}, beta: {number(fit.hawkes.beta)}}}")
    lines += [
        "    weights:",
    ]
    lines += [f"      {intent}: {weight:.6f}" for intent, weight in fit.weights.items()]
    lines += [
        "    hazards:",
        f"      dormancy: lognormal({number(dormancy.mu)}, {number(dormancy.sigma)})",
        f"      dormancy_onset: {number(onset)}",
    ]
    return lines


def _spike_lines(name: str, spike: SpikeFit) -> list[str]:
    return [
        f"      {name}:",
        f"        dist: {spike.dist}({number(spike.a)}, {number(spike.b)})",
        f"        point: {number(spike.point)}",
        f"        point_share: {number(spike.share)}",
    ]


def population_lines(actors: ActorFit) -> list[str]:
    """The `populations` and `network` blocks (internal/profile Population
    and Network); nothing for a chain with none of them."""
    lines = []
    if actors.pools:
        p = actors.pools
        lines += [
            "  - name: pools", "    kind: pool", f"    count: {p.count}", f"    chain_count: {p.chain_count}",
            "    pool:",
            f"      register_rate: {number(p.register_rate)}",
            f"      retire_rate: {number(p.retire_rate)}",
            f"      update_rate: {number(p.update_rate)}",
        ]
        if p.vote_rate:
            lines.append(f"      spo_vote_rate: {number(p.vote_rate)}")
        lines += _spike_lines("pledge", p.pledge) + _spike_lines("margin", p.margin) + _spike_lines("cost", p.cost)
    if actors.dreps:
        d = actors.dreps
        lines += [
            "  - name: dreps", "    kind: drep", f"    count: {d.count}", f"    chain_count: {d.chain_count}",
            "    drep:",
            f"      register_rate: {number(d.register_rate)}",
            f"      retire_rate: {number(d.retire_rate)}",
            f"      vote_rate: {number(d.vote_rate)}",
            f"      voting_share: {number(d.voting_share)}",
        ]
    if actors.contracts:
        c = actors.contracts
        lines += [
            "  - name: contracts", "    kind: contract", f"    chain_count: {c.chain_count}",
            "    contract:",
            f"      new_rate: {number(c.new_rate)}",
            f"      usage: pareto(xm=1, alpha={number(c.usage_alpha)})",
            f"      top_share: {number(c.top_share)}",
        ]
    if lines:
        lines = ["populations:"] + lines
    if actors.proposal_rate:
        lines += ["network:", "  proposal_rate:"]
        lines += [f"    {action}: {number(rate)}" for action, rate in actors.proposal_rate.items()]
    return lines


def target_of(stats: Stats, fit: PopulationFit) -> dict:
    """What a run of this population is graded against.

    Each KS statistic is stored as QUANTILE_POINTS quantiles of the measured
    sample, not as the fitted curve: a run is graded against what the chain
    did, so a poor fit shows up as a failing grade instead of hiding in
    its own target.
    """
    return {
        "info": stats.info,
        "fit": fit.as_json(),
        "quantiles": {name: quantiles(stats.samples[name]) for name in KS_STATS if len(stats.samples[name])},
        "shares": stats.shares,
        "fractions": stats.fractions,
    }


def _header_value(value) -> str:
    """A header value as text; a dict (the BIC curve) as `k=value` pairs."""
    if not isinstance(value, dict):
        return f"{value}"
    return ", ".join(f"{k}={v:.1f}" if isinstance(v, float) else f"{k}={v}" for k, v in value.items())


def header_lines(header: dict) -> list[str]:
    """The comment header of a clustered profile file: how it was made, so
    the fit can be repeated (window, seed, unit, weighting) and judged (the
    BIC curve and the chosen k)."""
    lines = ["# Fitted by lab/fit.py --clusters; see lab/README.md. Do not edit by hand."]
    lines += [f"# {key}: {_header_value(header[key])}" for key in HEADER_KEYS if key in header]
    return lines


def render_single(name: str, stats: Stats, fit: PopulationFit) -> tuple[str, dict]:
    """A one-profile file and its target."""
    lines = [
        "# Fitted by lab/fit.py from chain data; see lab/README.md for the mapping.",
        f"version: {PROFILE_VERSION}",
        "profiles:",
    ] + profile_lines(name, fit)
    return "\n".join(lines) + "\n", {"version": PROFILE_VERSION, **target_of(stats, fit)}


def render_clustered(
    overall: tuple[Stats, PopulationFit],
    clusters: dict[str, tuple[Stats, PopulationFit]],
    shares: dict[str, float],
    header: dict,
    actors: ActorFit | None = None,
) -> tuple[str, dict]:
    """A clustered profile file and its target.

    clusters maps each cluster name to its statistics and fit; shares gives
    each one's weight (they sum to 1). Profiles are written in name order.
    The target carries the overall statistics and each cluster's, so a run
    can be graded as a whole and per cluster. actors, when given, adds the
    non-wallet populations to both.
    """
    lines = header_lines(header) + [f"version: {PROFILE_VERSION}", "profiles:"]
    target = {"version": PROFILE_VERSION, "header": header, "overall": target_of(*overall), "clusters": {}}
    for name in sorted(clusters):
        stats, fit = clusters[name]
        lines += profile_lines(name, fit, shares[name])
        cluster_target = target_of(stats, fit)
        cluster_target["share"] = shares[name]
        target["clusters"][name] = cluster_target
    if actors is not None:
        lines += population_lines(actors)
        target["actors"] = actors.as_json()
    return "\n".join(lines) + "\n", target
