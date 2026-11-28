"""The figures of guide.ipynb and explore.ipynb.

Drawing only: the numbers come from guide_lib and the lab modules. A
function named show_* draws and displays (the notebook's sliders call
these); one named fig_* returns the figure for the caller to show.
"""

from __future__ import annotations

import zlib

import matplotlib.colors as mcolors
import matplotlib.pyplot as plt
import numpy as np
import pandas as pd
from scipy import stats as sps

import guide_lib
from cardano import LOVELACE_PER_ADA
from compare import KS_PASS_DISTANCE
from guide_lib import Network

BLUE, ORANGE, RED, GREEN, PURPLE = "#4c78a8", "#f58518", "#e45756", "#54a24b", "#b279a2"
LIGHT_BLUE, GREY = "#cfe3f4", "#888"

BEHAVIOUR_COLOURS = {"holder": BLUE, "occasional-payer": ORANGE, "frequent-trader": RED,
                     "script-user": GREEN, "staker": PURPLE}
# How far towards white a cluster's colour is mixed, by its size suffix.
SIZE_WHITENING = {"-small": 0.5, "-mid": 0.25}
SIZE_SUFFIXES = ("-small", "-mid", "-large")


def colour(name: str) -> tuple:
    """A stable colour per cluster name: one hue per behaviour class, lighter
    for smaller payments, so variants of a class stay distinguishable."""
    behaviour = next((b for b in BEHAVIOUR_COLOURS if name.startswith(b)), None)
    if behaviour is None:
        return plt.cm.tab10(zlib.crc32(name.encode()) % 10)
    suffix = next((s for s in SIZE_SUFFIXES if s in name), "")
    white = SIZE_WHITENING.get(suffix, 0.0)
    r, g, b = mcolors.to_rgb(BEHAVIOUR_COLOURS[behaviour])
    return (r + (1 - r) * white, g + (1 - g) * white, b + (1 - b) * white)


# --- section 1: two layers of randomness -------------------------------------


def show_two_layers(seed: int = 3) -> None:
    """Six actors over ten days: slow awake spells (bands) with fast
    Poisson wake-ups inside them (ticks)."""
    rng = np.random.default_rng(seed)
    fig, ax = plt.subplots(figsize=(10, 3))
    for actor in range(6):
        for start, end in guide_lib.awake_spells(rng, 10, onset_per_day=1.0, sleep_median_days=1.5):
            ax.broken_barh([(start, end - start)], (actor - 0.35, 0.7), color=LIGHT_BLUE)
            wakeups = guide_lib.poisson_times(rng, 6, start, end)
            ax.plot(wakeups, np.full(len(wakeups), actor), "|", color=ORANGE, ms=9)
    ax.set_xlabel("days")
    ax.set_ylabel("actor")
    ax.set_title("slow layer (bands) and fast layer (ticks)")
    plt.show()


# --- section 2: lifecycles and hazard rates ----------------------------------


def show_lifecycle(onset_per_day: float = 0.5, sleep_median_days: float = 2.0, seed: int = 1) -> None:
    """Ten actors over sixty days, and the lengths of their awake spells
    against the exponential density a constant hazard predicts:
    onset * exp(-onset * length)."""
    rng = np.random.default_rng(seed)
    fig, (timeline, histogram) = plt.subplots(1, 2, figsize=(11, 3.4), gridspec_kw={"width_ratios": [2, 1]})
    spell_lengths = []
    for actor in range(10):
        spells = guide_lib.awake_spells(rng, 60, onset_per_day, sleep_median_days)
        # The last spell is cut off by the end of the simulation, not by
        # falling asleep, so its length is left out.
        spell_lengths += [end - start for start, end in spells[:-1]]
        for start, end in spells:
            timeline.broken_barh([(start, end - start)], (actor - 0.35, 0.7), color=BLUE)
    timeline.set_xlabel("days")
    timeline.set_title("awake spells (blue), dormant gaps (white)")
    histogram.hist(spell_lengths, bins=25, density=True, color=LIGHT_BLUE, label="simulated spells")
    lengths = np.linspace(0, max(spell_lengths), 200)
    histogram.plot(lengths, onset_per_day * np.exp(-onset_per_day * lengths), color=RED, label="exponential")
    histogram.set_xlabel("spell length (days)")
    histogram.legend(fontsize=8)
    plt.show()


# --- section 3: the four distributions ---------------------------------------


def _draw_density(ax, x: np.ndarray, density: np.ndarray, title: str, xlabel: str) -> None:
    ax.fill_between(x, density, color=LIGHT_BLUE)
    ax.plot(x, density, color=BLUE)
    ax.set_title(title, fontsize=10)
    ax.set_xlabel(xlabel)
    ax.set_ylim(bottom=0)


def show_four_distributions(a: float = 8.0, b: float = 2.0, mu: float = 1.0, sigma: float = 0.5,
                            alpha: float = 1.3, xm: float = 1.0, rate: float = 1.0) -> None:
    """The densities of the four distributions a profile is written in.

    scipy parametrises them differently from a profile file: lognorm takes
    sigma as its shape and exp(mu) as its scale, pareto takes alpha as its
    shape and xm as its scale, expon takes 1/rate as its scale.
    """
    fig, axes = plt.subplots(1, 4, figsize=(15, 3))
    x = np.linspace(0.001, 0.999, 300)
    _draw_density(axes[0], x, sps.beta.pdf(x, a, b), f"Beta({a:g}, {b:g}); mean {a/(a+b):.2f}", "trait")
    x = np.linspace(0.01, 15, 400)
    _draw_density(axes[1], x, sps.lognorm.pdf(x, sigma, scale=np.exp(mu)),
                  f"Log-normal(mu={mu:g}, sigma={sigma:g})", "value")
    x = np.linspace(xm, xm * 12, 400)
    _draw_density(axes[2], x, sps.pareto.pdf(x, alpha, scale=xm), f"Pareto(alpha={alpha:g}, xm={xm:g})", "value")
    x = np.linspace(0, 6 / rate, 300)
    _draw_density(axes[3], x, sps.expon.pdf(x, scale=1 / rate),
                  f"Exponential(rate={rate:g}); mean wait {1/rate:.2f}", "wait")
    fig.tight_layout()
    plt.show()


# --- section 4: when things happen -------------------------------------------


def show_arrivals(rate: float = 1.0, daily: float = 0.0, alpha: float = 0.0, beta: float = 2.0,
                  seed: int = 4) -> None:
    """Three simulated days of wake-ups and the rate that produced them."""
    beta = max(beta, alpha * 1.05 + 0.05)   # keep alpha < beta
    events, grid, intensity = guide_lib.arrivals(seed, 72, rate, daily, alpha, beta)
    fig, ax = plt.subplots(figsize=(11, 3))
    ax.plot(grid, intensity, color=BLUE, lw=1.2, label="rate (per hour)")
    ax.plot(events, np.zeros(len(events)), "|", color=ORANGE, ms=14, label=f"{len(events)} wake-ups")
    ax.set_xlabel("hours")
    ax.set_ylim(bottom=-0.1)
    ax.legend(loc="upper right", fontsize=8)
    ax.set_title(f"three simulated days; alpha/beta = {alpha/beta:.2f}" + (" (bursts)" if alpha else ""))
    plt.show()


# --- section 6: calibration --------------------------------------------------


def _pass_or_fail(distance: float, bound: float) -> str:
    return "pass" if distance <= bound else "fail"


def show_ks_demo(shift: float = 0.15, n: int = 2000) -> None:
    """Two bell-curve samples, one shifted: their cumulative curves, the
    KS distance D between them and the p-value for comparison."""
    rng = np.random.default_rng(2)
    real, simulated = rng.normal(0, 1, n), rng.normal(shift, 1, n)
    distance, widest_at = guide_lib.ks_gap(real, simulated)
    p_value = sps.ks_2samp(real, simulated).pvalue
    fig, ax = plt.subplots(figsize=(7, 3.4))
    for sample, line_colour, label in ((real, BLUE, "real"), (simulated, ORANGE, "simulated")):
        ax.step(*guide_lib.ecdf(sample), color=line_colour, label=label)
    ax.axvline(widest_at, color=GREY, ls=":")
    ax.set_title(f"D = {distance:.3f} ({_pass_or_fail(distance, KS_PASS_DISTANCE)});  "
                 f"p-value = {p_value:.2g}  (n = {n})", fontsize=10)
    ax.legend()
    plt.show()


def fig_ks(run: dict[str, np.ndarray], target: dict, dmax: float = KS_PASS_DISTANCE):
    """Run samples against the target's quantile curve, with D marked."""
    names = [name for name in ("log10_output_value", "log10_gap_seconds", "activity_per_day")
             if len(run.get(name, [])) and name in target["quantiles"]]
    fig, axes = plt.subplots(1, len(names), figsize=(4.6 * len(names), 3.5), squeeze=False)
    for ax, name in zip(axes[0], names):
        target_quantiles = np.asarray(target["quantiles"][name])
        distance, widest_at = guide_lib.ks_gap(run[name], target_quantiles)
        values, shares = guide_lib.ecdf(run[name])
        ax.step(values, shares, color=ORANGE, label=f"run (n={len(values)})")
        ax.plot(target_quantiles, np.linspace(0, 1, len(target_quantiles)), color=BLUE, label="target")
        ax.axvline(widest_at, color=GREY, ls=":")
        ax.set_title(f"{name}\nD = {distance:.3f} ({_pass_or_fail(distance, dmax)}, bound {dmax})", fontsize=9)
        if name == "activity_per_day":
            ax.set_xscale("log")
        ax.legend(fontsize=7)
    fig.tight_layout()
    return fig


def fig_fit_histograms(samples: dict[str, np.ndarray]):
    """The three samples a single-profile fit rests on, on log scales."""
    fig, axes = plt.subplots(1, 3, figsize=(14, 3.5))
    axes[0].hist(samples["log10_output_value"], bins=80)
    axes[0].set_title("log10 output value (lovelace)")
    axes[1].hist(samples["log10_gap_seconds"], bins=80)
    axes[1].set_title("log10 inter-tx gap (s)")
    axes[2].hist(np.log10(samples["activity_per_day"]), bins=60)
    axes[2].set_title("log10 txs per address per day")
    fig.tight_layout()
    return fig


def fig_run_against_target(run: dict[str, np.ndarray], target: dict):
    """Histograms of a run's samples over the target's quantiles."""
    names = ["log10_output_value", "log10_gap_seconds"]
    fig, axes = plt.subplots(1, len(names), figsize=(10, 3.5))
    for ax, name in zip(axes, names):
        ax.hist(target["quantiles"][name], bins=60, alpha=0.5, density=True, label="target")
        ax.hist(run[name], bins=60, alpha=0.5, density=True, label="run")
        ax.set_title(name)
        ax.legend()
    fig.tight_layout()
    return fig


# --- section 7: clustering ---------------------------------------------------

SCATTER_SAMPLE = 6000


def fig_units_scatter(table: pd.DataFrame, real: bool):
    """An interactive scatter of units, coloured by cluster: activity
    against median payment, both on log scales."""
    import plotly.express as px

    sample = table.sample(min(len(table), SCATTER_SAMPLE), random_state=1)
    source = "real preprod, last 90 days" if real else "fixture"
    return px.scatter(
        sample,
        x=np.log10(sample.tx_per_day),
        y=np.log10(sample.median_payment_lovelace / LOVELACE_PER_ADA),
        color="cluster", opacity=0.5,
        labels={"x": "log10 transactions per day", "y": "log10 median payment (ADA)"},
        title=f"Units and clusters ({source})", height=480,
    )


def fig_weighting(by_units: pd.Series, by_transactions: pd.Series):
    """Cluster shares by units next to shares by transactions."""
    fig, ax = plt.subplots(figsize=(7, 3.4))
    positions = np.arange(len(by_units))
    ax.bar(positions - 0.2, by_units.values, 0.4, label="by units")
    ax.bar(positions + 0.2, by_transactions.values, 0.4, label="by transactions")
    ax.set_xticks(positions)
    ax.set_xticklabels(by_units.index, rotation=30, ha="right", fontsize=7)
    ax.legend()
    ax.set_title("weighting changes the mix")
    fig.tight_layout()
    return fig


def bic_curves(this_fit, nets: list[Network]) -> tuple[dict[str, dict[int, float]], dict[str, int]]:
    """The BIC curve and chosen k of a fresh fit and of each shipped fit."""
    curves = {"this fit": this_fit.bic}
    chosen = {"this fit": this_fit.k}
    for net in nets:
        if net.fitted:
            curves[f"shipped {net.name}"] = guide_lib.bic_curve(net.header)
            chosen[f"shipped {net.name}"] = net.mixture_k
    return curves, chosen


def fig_bic(curves: dict[str, dict[int, float]], chosen: dict[str, int]):
    """BIC against k with the chosen k marked, one panel per fit."""
    fig, axes = plt.subplots(1, len(curves), figsize=(4.6 * len(curves), 3.3), squeeze=False)
    for ax, (name, curve) in zip(axes[0], curves.items()):
        ks = sorted(curve)
        ax.plot(ks, [curve[k] for k in ks], "o-", ms=3)
        if chosen[name] in curve:
            ax.plot([chosen[name]], [curve[chosen[name]]], "o", color=RED, ms=8, label=f"chosen k = {chosen[name]}")
        ax.set_title(name, fontsize=10)
        ax.set_xlabel("k (mixture components)")
        ax.set_ylabel("BIC (lower is better)")
        ax.legend(fontsize=8)
    fig.tight_layout()
    return fig


# --- section 8: what each network looks like ---------------------------------


def _mark_unfitted(ax, name: str, fontsize: float | None = None) -> None:
    ax.text(0.5, 0.5, f"{name}: not fitted yet", ha="center", va="center", color="#777",
            fontsize=fontsize, transform=ax.transAxes)
    ax.set_xticks([])
    ax.set_yticks([])


def panels(title: str, nets: list[Network], size=(4.6, 3.4)):
    """One axes per network, side by side. An unfitted network gets a labelled
    placeholder, so a mainnet fit appears the day its file does."""
    fig, axes = plt.subplots(1, len(nets), figsize=(size[0] * len(nets), size[1]), squeeze=False)
    for ax, net in zip(axes[0], nets):
        ax.set_title(f"{net.name}", fontsize=10)
        if net.fitted:
            continue
        _mark_unfitted(ax, net.name, fontsize=11)
        for spine in ax.spines.values():
            spine.set_linestyle("--")
            spine.set_color("#aaa")
    fig.suptitle(title, fontsize=11)
    return fig, axes[0]


def fig_shares(nets: list[Network]):
    """Cluster shares by units (the weight in each profile file)."""
    fig, axes = panels("Cluster shares (weighted by units)", nets)
    for ax, net in zip(axes, nets):
        if not net.fitted:
            continue
        weights = [profile["weight"] for profile in net.profiles]
        ax.barh(net.names, weights, color=[colour(name) for name in net.names])
        ax.invert_yaxis()
        ax.set_xlabel("share of units")
    fig.tight_layout()
    return fig


def fig_quantile_cdfs(nets: list[Network], stat: str, xlabel: str, title: str, logx: bool = False):
    """Per-cluster empirical CDFs of a target statistic, drawn from its
    quantiles: the q-th quantile is plotted at height q."""
    fig, axes = panels(title, nets)
    for ax, net in zip(axes, nets):
        for name, cluster_target in net.target.get("clusters", {}).items():
            quantile_values = cluster_target["quantiles"].get(stat)
            if quantile_values:
                ax.plot(quantile_values, np.linspace(0, 1, len(quantile_values)), color=colour(name), label=name,
                        lw=1.3)
        if not net.fitted:
            continue
        ax.set_xlabel(xlabel)
        if logx:
            ax.set_xscale("log")
        ax.set_ylabel("share at or below")
        ax.legend(fontsize=6, loc="lower right")
    fig.tight_layout()
    return fig


def fig_amounts(nets: list[Network]):
    """Payment-amount curves: log-normal body, Pareto tail, per cluster."""
    fig, axes = panels("Payment amounts: body + tail", nets)
    log10_lovelace = np.linspace(5.5, 13.5, 500)
    ada = 10 ** log10_lovelace / LOVELACE_PER_ADA
    for ax, net in zip(axes, nets):
        for name, cluster_target in net.target.get("clusters", {}).items():
            density = guide_lib.amount_density(cluster_target["fit"]["amount"], log10_lovelace)
            ax.plot(ada, density, color=colour(name), label=name, lw=1.3)
        if not net.fitted:
            continue
        ax.set_xscale("log")
        ax.set_ylim(0, 2.5)  # payments that pile on one value (2 ADA) peak far above
        ax.set_xlabel("payment (ADA)")
        ax.set_ylabel("density per decade")
        ax.legend(fontsize=6)
    fig.tight_layout()
    return fig


def fig_mix(nets: list[Network]):
    """Transaction-type mix on chain per cluster, as stacked bars, with the
    agenda each cluster was given (agendas.choose_agenda) in its label."""
    fig, axes = panels("Transaction-type mix (and agenda)", nets)
    kinds = ["plain", "script", "stake", "pool", "gov"]
    colours = [BLUE, GREEN, PURPLE, RED, ORANGE]
    for ax, net in zip(axes, nets):
        if not net.fitted:
            continue
        clusters = net.target.get("clusters", {})
        labels = [f"{profile['name']} ({profile['agenda']})" for profile in net.profiles]
        left = np.zeros(len(net.profiles))
        for kind, kind_colour in zip(kinds, colours):
            shares = np.array([clusters.get(name, {}).get("shares", {}).get("tx_type_mix", {}).get(kind, 0.0)
                               for name in net.names])
            ax.barh(labels, shares, left=left, color=kind_colour, label=kind)
            left += shares
        ax.invert_yaxis()
        ax.legend(fontsize=6, loc="lower right")
    fig.tight_layout()
    return fig


def _draw_traits(row, profile: dict) -> None:
    """One profile's competence and care (beta) and patience (pareto)."""
    trait_grid = np.linspace(0.005, 0.995, 300)
    line_colour = colour(profile["name"])
    for ax, trait in zip(row[:2], ("competence", "care")):
        _, (a, b), _ = guide_lib.parse_dist(profile["traits"][trait])
        # A beta density can shoot to infinity at 0 or 1; cap it so the
        # other curves stay visible.
        ax.plot(trait_grid, np.clip(sps.beta.pdf(trait_grid, a, b), 0, 8), color=line_colour, lw=1.2)
    _, _, patience = guide_lib.parse_dist(profile["traits"]["patience"])
    decades = np.log10(patience["xm"])
    seconds = np.logspace(decades, decades + 4, 200)
    # Density times x: the density per unit of ln x, which is what a
    # log-scaled axis shows as area.
    density = sps.pareto.pdf(seconds, patience["alpha"], scale=patience["xm"]) * seconds
    row[2].plot(seconds, density, color=line_colour, lw=1.2)


def fig_traits(nets: list[Network]):
    """Trait shapes of each cluster: competence and care (beta), patience (pareto)."""
    fig, axes = plt.subplots(len(nets), 3, figsize=(13, 2.8 * len(nets)), squeeze=False)
    for row, net in zip(axes, nets):
        if not net.fitted:
            for ax in row:
                _mark_unfitted(ax, net.name)
            continue
        for profile in net.profiles:
            _draw_traits(row, profile)
        row[0].set_ylabel(net.name)
        row[2].set_xscale("log")
    titles = ("competence ~ beta (0..1)", "care ~ beta (0..1)", "patience ~ pareto (s, density x s)")
    for ax, title in zip(axes[0], titles):
        ax.set_title(title, fontsize=9)
    fig.tight_layout()
    return fig


# --- section 9: currencies and exchanges ---------------------------------------


def show_rate_path(seed: int = 4, hours: float = 6.0) -> None:
    """ADA in each currency rebased to 100, and one currency's bids on each
    exchange: the spreads and the drifting basis."""
    path = guide_lib.rate_path(seed, hours)
    fig, (left, right) = plt.subplots(1, 2, figsize=(11, 3.4))
    for c in path.params["currencies"]:
        mid = path.mid(c["code"])
        left.plot(path.t_hours, 100 * mid / mid[0], lw=1, label=c["code"])
    peak, trough, depth = guide_lib.largest_drawdown(path.ada)
    left.axvspan(path.t_hours[peak], path.t_hours[trough], color=LIGHT_BLUE, label=f"drawdown {100 * depth:.0f}%")
    left.set_xlabel("hours")
    left.set_ylabel("ADA, rebased to 100")
    left.set_title("one seeded market path")
    left.legend(fontsize=7, ncol=2)
    code = "USD"
    for x, col in zip(path.params["exchanges"], (BLUE, ORANGE, GREEN)):
        if code not in x["currencies"]:
            continue
        bid, ask = path.quotes(x, code)
        right.plot(path.t_hours, bid, color=col, lw=1, label=f"{x['name']} bid")
        right.plot(path.t_hours, ask, color=col, lw=0.6, ls="--", label=f"{x['name']} ask")
    end = len(path.t_hours) // 6
    right.set_xlim(0, path.t_hours[end])
    right.set_xlabel("hours (first sixth)")
    right.set_ylabel("USD per ADA")
    right.set_title("exchanges quote slightly different rates")
    right.legend(fontsize=7)
    fig.tight_layout()
    plt.show()


def show_extra_currencies(extra: float = 0.15, seed: int = 2) -> None:
    """Extra preferred currencies per wallet against (1-q) q^k."""
    params = guide_lib.markets_params()
    n = len(params["currencies"])
    drawn = guide_lib.extra_currencies(seed, 5000, extra, n)
    ks = np.arange(n)
    fig, ax = plt.subplots(figsize=(6, 3))
    ax.bar(ks, np.bincount(drawn, minlength=n)[:n] / len(drawn), color=LIGHT_BLUE, label="5,000 drawn wallets")
    ax.plot(ks, (1 - extra) * extra ** ks, "o-", color=ORANGE, label="(1-q) q^k")
    ax.set_yscale("log")
    ax.set_xlabel("extra currencies beside the primary")
    ax.set_ylabel("share of wallets")
    ax.set_title(f"most wallets hold one currency (q = {extra:.2f})")
    ax.legend()
    plt.show()


def show_lifecycle_factors(seed: int = 4, hours: float = 6.0) -> None:
    """The synthetic birth and death factors one currency's path gives."""
    path = guide_lib.rate_path(seed, hours)
    code = "EUR"
    fig, ax = plt.subplots(figsize=(10, 3))
    mid = path.mid(code)
    ax.plot(path.t_hours, mid / mid[0], color=GREY, lw=1, label=f"ADA in {code}, rebased to 1")
    ax.plot(path.t_hours, guide_lib.birth_factor(path, code), color=GREEN, lw=1, label="birth factor")
    ax.plot(path.t_hours, guide_lib.death_factor(path, code), color=RED, lw=1, label="death factor")
    ax.plot(path.t_hours, guide_lib.death_factor(path, code, hodl=True), color=RED, lw=1, ls=":", label="death factor, hodl")
    ax.set_xlabel("hours")
    ax.set_ylabel("multiplier on the hazard")
    ax.set_title("births follow gains, deaths a prolonged fall")
    ax.legend(fontsize=7, ncol=2)
    plt.show()


def show_drawdown_split(depth: float = -0.3, seed: int = 5) -> None:
    """Who sells and who holds when the market is depth under their cost."""
    split = guide_lib.drawdown_split(seed, depth=depth)
    fig, ax = plt.subplots(figsize=(6, 3))
    labels = ["hodl wallets\n(hold by agenda)", "traders who held", "traders who sold"]
    ax.bar(labels, [split["hodl"], split["held"], split["sold"]], color=[BLUE, LIGHT_BLUE, RED])
    ax.set_ylabel("wallets")
    ax.set_title(f"{100 * depth:.0f}% under water, twenty decisions each")
    plt.show()
