"""Estimators: turn a sample into the parameters of a distribution.

Everything here is pure arithmetic on arrays. No files, no database.

Two ways of fitting are used, and each function says which:

**Maximum likelihood (MLE).** Pick the parameters under which the observed
data would have been most probable. If the density is f(x; theta), the
likelihood of independent observations x_1..x_n is the product of
f(x_i; theta); we maximise its logarithm,

    log L(theta) = sum_i log f(x_i; theta)

For the log-normal and the Pareto the maximum has a closed formula; for the
truncated log-normal it has none and is found numerically.
https://en.wikipedia.org/wiki/Maximum_likelihood_estimation

**Method of moments.** Pick the parameters whose theoretical mean and
variance equal the sample's mean and variance. Cruder than MLE, but it is
one line of algebra and cannot fail to converge. Used for the beta.
https://en.wikipedia.org/wiki/Method_of_moments_(statistics)

In every formula, ln is the natural logarithm and n the sample size.

Guide: section 3 of guide.ipynb ("The four distributions") shows each curve
with sliders; section 6 ("Calibration") says where the fits are used.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
from scipy import stats as sps
from scipy.optimize import minimize

# Where the body of a payment-size distribution ends and its tail begins:
# the smallest 90% of payments are the log-normal body, the largest 10% the
# Pareto tail. A tail needs enough points to estimate a slope (10% of even a
# small cluster is a few dozen) and must start late enough to be past the
# bulk. A lower quantile starts the tail earlier (smaller xm, more points,
# but some of the bulk is then fitted as a power law); a higher one leaves
# fewer points and a noisier alpha. The effect on the shipped fits has not
# been measured. Grading compares a run against the data's own quantiles,
# so the target does not depend on this choice; only the profile does.
TAIL_QUANTILE = 0.9

# What a fit returns when the sample is too small or degenerate to say
# anything: a standard log-normal, a Pareto with a finite mean and infinite
# variance, and a flat beta. Neutral shapes that are valid in a profile.
DEFAULT_LOGNORMAL = (0.0, 1.0)
DEFAULT_PARETO_INDEX = 2.0
UNIFORM_BETA = (1.0, 1.0)

# A sample of identical values has zero spread, and sigma = 0 is not a
# distribution the profile format accepts. This is the smallest sigma used.
MIN_SIGMA = 1e-6

# Below this many points the numerical truncated fit is not attempted: two
# parameters and a correction term from a handful of values would chase
# noise, so the plain log-normal fit is returned.
MIN_POINTS_FOR_CUT_FIT = 50

# Search limits for sigma in the truncated fit (see fit_cut_lognormal). A
# sigma of 10 means payments spread over e^10, about 22,000-fold, per
# standard deviation; no payment distribution fitted here comes close, so
# the limits only stop a runaway search.
MIN_CUT_SIGMA = 1e-3
MAX_CUT_SIGMA = 10.0
# How far below the smallest observed ln x the mean may be placed.
CUT_MU_SLACK = 10.0

# A variance at or below this is treated as "all values equal" (beta fit).
MIN_BETA_VARIANCE = 1e-9
MIN_POINTS_FOR_BETA = 3


def fit_lognormal(sample: np.ndarray) -> tuple[float, float]:
    """Log-normal by maximum likelihood: returns (mu, sigma).

    Intuition: a log-normal is a bell curve on a log scale. Take logs, then
    measure the centre and the spread.

        X ~ LogNormal(mu, sigma)   means   ln X ~ Normal(mu, sigma)
        mu_hat    = (1/n) sum_i ln x_i                    centre of the logs
        sigma_hat = sqrt( (1/n) sum_i (ln x_i - mu_hat)^2 )   their spread

    Median of X is exp(mu); mean is exp(mu + sigma^2 / 2).

    Assumes: values are positive (others are dropped) and independent.
    What could go wrong: real data is rarely a clean log-normal. A spike of
    identical values (the 2 ADA pile) pulls mu towards it and shrinks sigma;
    a heavy tail inflates sigma. That is why payments use fit_body_and_tail.
    Fewer than two positive values return DEFAULT_LOGNORMAL.

    https://en.wikipedia.org/wiki/Log-normal_distribution#Estimation_of_parameters
    https://docs.scipy.org/doc/scipy/reference/generated/scipy.stats.lognorm.html
    """
    positive = sample[sample > 0]
    if len(positive) < 2:
        return DEFAULT_LOGNORMAL
    logs = np.log(positive)
    return float(logs.mean()), float(max(logs.std(), MIN_SIGMA))


def fit_pareto_index(sample: np.ndarray, xm: float) -> float:
    """Pareto tail index alpha above the scale xm, by maximum likelihood.

    Intuition: a Pareto says "the chance of exceeding x falls like a power
    of x". alpha is how fast: small alpha, heavy tail, a few giants hold
    most of the total.

        P(X > x) = (xm / x)^alpha            for x >= xm
        alpha_hat = n / sum_i ln(x_i / xm)   over the n values above xm

    The sum is the total distance, on a log scale, by which the values
    exceed xm; a tail that reaches far has a large sum and a small alpha.
    With alpha <= 1 the mean is infinite, with alpha <= 2 the variance is.

    Assumes: values above xm really follow a power law, and xm is given.
    What could go wrong: only values strictly above xm are used, so a
    sample that sits on xm (integer counts, tied amounts) fits its tail
    from what is left. Fewer than two such values, or a non-positive xm,
    return DEFAULT_PARETO_INDEX.

    This is the Hill estimator.
    https://en.wikipedia.org/wiki/Pareto_distribution#Estimation_of_parameters
    https://en.wikipedia.org/wiki/Hill_estimator
    """
    above = sample[sample > xm]
    if len(above) < 2 or xm <= 0:
        return DEFAULT_PARETO_INDEX
    total_log_excess = float(np.log(above / xm).sum())
    if total_log_excess <= 0:
        return DEFAULT_PARETO_INDEX
    return float(len(above) / total_log_excess)


def cut_lognormal_cost(mu: float, sigma: float, logs: np.ndarray, log_upper: float) -> float:
    """Negative log-likelihood of a log-normal cut off above at upper,
    without the terms that do not depend on mu or sigma.

    With y = ln x, b = ln upper, z_i = (y_i - mu) / sigma and Phi the
    standard normal cumulative curve:

        cost(mu, sigma) = sum_i [ ln sigma + z_i^2 / 2 ]  +  n ln Phi((b - mu) / sigma)

    The sum is the ordinary normal cost. The last term is the price of the
    cut: every observation is known to lie below b, and Phi((b - mu)/sigma)
    is the share of the full curve that does. Dividing by it (adding its
    log, in a negative log-likelihood) rescales the kept part to total
    probability 1.
    """
    z = (logs - mu) / sigma
    normal_cost = np.sum(math.log(sigma) + 0.5 * z * z)
    cut_cost = len(logs) * sps.norm.logcdf((log_upper - mu) / sigma)
    return float(normal_cost + cut_cost)


def fit_cut_lognormal(sample: np.ndarray, upper: float) -> tuple[float, float]:
    """Log-normal truncated above at upper, by maximum likelihood: (mu, sigma)
    of the *uncut* curve.

    Intuition: we only kept values up to upper (the body of the payments).
    Chop the right side off a bell curve and what is left looks narrower
    and centred further left than the curve it came from. Fitting the
    leftover naively (fit_lognormal) therefore understates both mu and
    sigma. This fit asks instead: which full curve, once cut at upper,
    best explains what we kept?

    There is no formula for the answer, so cut_lognormal_cost is minimised
    numerically, searching over (mu, ln sigma) from the naive fit. Searching
    ln sigma keeps sigma positive.

    Assumes: the values at or below upper are a log-normal cut at upper.
    What could go wrong: when many values sit exactly on upper (ties at a
    quantile, the 2 ADA pile) the likelihood has no finite maximum: mu and
    sigma can grow together forever, each step fitting slightly better. The
    search is boxed in to stop that (mu at most ln upper, sigma within
    [MIN_CUT_SIGMA, MAX_CUT_SIGMA]), and the result is then a bound, not an
    estimate. Under MIN_POINTS_FOR_CUT_FIT values, or if the search fails,
    the naive fit is returned.

    https://en.wikipedia.org/wiki/Truncated_normal_distribution
    https://docs.scipy.org/doc/scipy/reference/generated/scipy.optimize.minimize.html
    """
    kept = sample[(sample > 0) & (sample <= upper)]
    naive_mu, naive_sigma = fit_lognormal(kept)
    if len(kept) < MIN_POINTS_FOR_CUT_FIT:
        return naive_mu, naive_sigma
    logs, log_upper = np.log(kept), math.log(upper)

    def cost(params: np.ndarray) -> float:
        return cut_lognormal_cost(params[0], math.exp(params[1]), logs, log_upper)

    mu_bounds = (float(logs.min()) - CUT_MU_SLACK, log_upper)
    log_sigma_bounds = (math.log(MIN_CUT_SIGMA), math.log(MAX_CUT_SIGMA))
    start_mu = min(naive_mu, log_upper)
    start_log_sigma = min(max(math.log(naive_sigma), log_sigma_bounds[0]), log_sigma_bounds[1])
    # Nelder-Mead needs no derivatives and tolerates the flat, ridge-shaped
    # cost that ties produce.
    result = minimize(cost, [start_mu, start_log_sigma], method="Nelder-Mead",
                      bounds=[mu_bounds, log_sigma_bounds],
                      options={"xatol": 1e-6, "fatol": 1e-9, "maxiter": 2000})
    if not np.isfinite(result.fun):
        return naive_mu, naive_sigma
    return float(result.x[0]), float(math.exp(result.x[1]))


@dataclass(frozen=True)
class BodyTail:
    """A two-piece distribution: log-normal body, Pareto tail.

    Field order is the key order of "amount" in target.json.
    """

    # Where the body ends and the tail starts, in the sample's unit
    # (lovelace for payments): the q-quantile of the sample.
    xm: float
    # Log-normal body, on the natural-log scale: ln x ~ Normal(mu, sigma).
    mu: float
    sigma: float
    # Pareto tail index above xm. Smaller is heavier.
    alpha: float
    # The quantile used for xm.
    q: float
    # Share of the sample strictly above xm; 0 means no tail.
    tail_share: float


def fit_body_and_tail(sample: np.ndarray, q: float = TAIL_QUANTILE) -> BodyTail:
    """Log-normal body up to the q-quantile, Pareto tail above it.

    Intuition: most payments are ordinary and cluster around a typical size
    (a bell curve on a log scale), while a few are enormous and follow a
    power law. Neither curve fits both, so each gets the part it is good at:

        with probability 1 - tail_share:  X ~ LogNormal(mu, sigma) cut at xm
        with probability tail_share:      X ~ Pareto(xm, alpha)

    tail_share is the fraction of the sample *strictly* above xm, which is
    what the profile's amount.tail_share means. Payment sizes pile up on
    exact values (the 2 ADA pile), so the quantile can land on a tie;
    counting only strictly larger values keeps that mass in the body, and
    the share is zero when nothing is larger.

    What could go wrong: the two pieces are fitted separately and need not
    join smoothly at xm; and the tail's alpha comes from only (1 - q) of the
    data, so it is the noisiest number in a profile.

    https://en.wikipedia.org/wiki/Mixture_distribution
    """
    xm = float(np.quantile(sample, q))
    mu, sigma = fit_cut_lognormal(sample[sample <= xm], xm)
    tail_share = float((sample > xm).mean())
    return BodyTail(xm=xm, mu=mu, sigma=sigma, alpha=fit_pareto_index(sample, xm), q=q, tail_share=tail_share)


def fit_beta(fractions: np.ndarray) -> tuple[float, float]:
    """Beta by the method of moments: returns (a, b).

    Intuition: a beta describes a quantity between 0 and 1, such as "the
    share of this user's transactions that return change". Think of a as
    the weight of evidence for 1 and b for 0: a/(a+b) is the average, and
    the larger a+b, the more alike the users are.

        mean      m = a / (a + b)
        variance  v = m (1 - m) / (a + b + 1)

    Solving those two equations for a and b, with m and v measured from the
    sample:

        k = m (1 - m) / v - 1        (this is a + b)
        a = m k
        b = (1 - m) k

    Why moments and not maximum likelihood: per-user fractions are often
    exactly 0 or 1, where the beta log-likelihood is infinite. Mean and
    variance are always defined.

    Assumes: the values are fractions in [0, 1].
    What could go wrong: v can never exceed m (1 - m) for a real beta, and
    when most values are exactly 0 or 1 it comes close: a and b then both
    fall below 1 and the fitted curve is U-shaped, saying "users are either
    always or never like this". That is a faithful summary, but far from
    the bell shape the letters a and b suggest. A sample that is too small,
    constant, or outside those limits returns UNIFORM_BETA.

    https://en.wikipedia.org/wiki/Beta_distribution#Method_of_moments
    https://docs.scipy.org/doc/scipy/reference/generated/scipy.stats.beta.html
    """
    finite = fractions[np.isfinite(fractions)]
    if len(finite) < MIN_POINTS_FOR_BETA:
        return UNIFORM_BETA
    mean, variance = float(finite.mean()), float(finite.var())
    largest_beta_variance = mean * (1 - mean)
    if variance <= MIN_BETA_VARIANCE or not 0 < mean < 1 or variance >= largest_beta_variance:
        return UNIFORM_BETA
    concentration = mean * (1 - mean) / variance - 1
    return float(mean * concentration), float((1 - mean) * concentration)
