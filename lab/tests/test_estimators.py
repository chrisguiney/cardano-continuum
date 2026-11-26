"""estimators.py: each fit recovers the parameters of data drawn from the
distribution it assumes, and degrades safely on data that breaks it."""

import numpy as np

import estimators
from cardano import MIN_OUTPUT_PILE_LOVELACE


def test_pareto_fit_recovers_the_tail_index():
    """Samples drawn from Pareto(xm=10, alpha=2.5) by inverting its
    cumulative curve give back alpha."""
    rng = np.random.default_rng(1)
    sample = 10.0 * (1 - rng.random(200_000)) ** (-1 / 2.5)
    assert abs(estimators.fit_pareto_index(sample, 10.0) - 2.5) < 0.05


def test_lognormal_fit_recovers_mu_and_sigma():
    rng = np.random.default_rng(2)
    mu, sigma = estimators.fit_lognormal(rng.lognormal(3.0, 0.7, 100_000))
    assert abs(mu - 3.0) < 0.02 and abs(sigma - 0.7) < 0.02


def test_beta_fit_recovers_a_and_b():
    rng = np.random.default_rng(3)
    a, b = estimators.fit_beta(rng.beta(5, 3, 100_000))
    assert abs(a - 5) < 0.2 and abs(b - 3) < 0.15


def test_cut_lognormal_fit_recovers_the_uncut_curve():
    """Keeping only the lowest 90% of a log-normal narrows the sample. The
    truncated fit still returns the parameters of the whole curve, where
    the plain fit of the kept part understates the spread."""
    rng = np.random.default_rng(8)
    sample = rng.lognormal(10.0, 2.0, 400_000)
    cut = float(np.quantile(sample, 0.9))
    body = sample[sample < cut]
    mu, sigma = estimators.fit_cut_lognormal(body, cut)
    assert abs(mu - 10.0) < 0.05 and abs(sigma - 2.0) < 0.05
    _, naive_sigma = estimators.fit_lognormal(body)
    assert naive_sigma < 1.8


def test_cut_lognormal_fit_stays_bounded_when_every_value_is_the_cut():
    """A thousand identical 2 ADA payments have no finite best fit (mu and
    sigma can grow together without limit); the search must stop inside its
    bounds instead of diverging."""
    pile = np.full(1000, float(MIN_OUTPUT_PILE_LOVELACE))
    mu, sigma = estimators.fit_cut_lognormal(pile, float(MIN_OUTPUT_PILE_LOVELACE))
    assert mu <= np.log(MIN_OUTPUT_PILE_LOVELACE)
    assert 0 < sigma <= estimators.MAX_CUT_SIGMA


def test_body_and_tail_has_no_tail_when_nothing_exceeds_the_quantile():
    """When the 90% quantile lands on a tie that reaches the maximum, no
    payment is strictly larger, so the tail share is zero and not 10%."""
    pile = np.full(1000, float(MIN_OUTPUT_PILE_LOVELACE))
    assert estimators.fit_body_and_tail(pile).tail_share == 0.0
