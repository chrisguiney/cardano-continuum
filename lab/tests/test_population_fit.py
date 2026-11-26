"""population_fit.py: how the fitted numbers of a profile follow from a
population's statistics."""

import numpy as np

import population_fit
from cardano import MIN_OUTPUT_PILE_LOVELACE
from helpers import synthetic_stats
from profile_yaml import profile_lines


def test_profile_omits_the_tail_when_payments_are_all_one_value():
    """A population that only ever pays 2 ADA gets an amount with a body
    and no tail lines: a tail with share zero would never be drawn."""
    stats = synthetic_stats(1, 16.0)
    stats.samples["output_value_lovelace"] = np.full(1000, float(MIN_OUTPUT_PILE_LOVELACE))
    text = "\n".join(profile_lines("p", population_fit.fit_population(stats)))
    assert "      tail:" not in text and "amount:\n      body: lognormal(" in text


def test_dormancy_onset_leaves_the_active_fraction():
    """Alternating active spells of mean 1/onset with dormancies of mean
    exp(mu + sigma^2/2) must leave the unit active for the requested share
    of the time; a unit that is always active gets the minimum onset."""
    mu, sigma, active_fraction = 12.0, 0.5, 0.05
    onset = population_fit.dormancy_onset(active_fraction, mu, sigma)
    mean_active = 1 / onset
    mean_dormant = np.exp(mu + sigma**2 / 2)
    assert abs(mean_active / (mean_active + mean_dormant) - active_fraction) < 1e-9
    assert population_fit.dormancy_onset(1.0, mu, sigma) == population_fit.MIN_ONSET


def test_payment_rate_is_per_active_second():
    """A unit active a tenth of the time must send ten times faster while
    active to reach the same count over the window."""
    stats = synthetic_stats(1, 16.0)
    stats.info["active_fraction"] = 0.1
    rarely_active = population_fit.fit_payment_activity(stats)
    stats.info["active_fraction"] = 1.0
    always_active = population_fit.fit_payment_activity(stats)
    assert abs(rarely_active.payments_per_sec / always_active.payments_per_sec - 10.0) < 1e-9


def test_wakeup_rate_compensates_for_the_hodl_send_score():
    """The engine turns only a share p of wake-ups into payments, so the
    fitted base rate is the payment rate divided by p, with p from the
    hodl agenda's send score (0.02) and the profile's send weight (0.9)."""
    stats = synthetic_stats(1, 16.0)
    fit = population_fit.fit_population(stats)
    p_send = population_fit.send_probability(population_fit.intent_weights(stats.shares["tx_type_mix"])["send"])
    assert abs(p_send - 0.02 * 0.9 / (1 + 0.02 * 0.9)) < 1e-12
    payments_per_sec = population_fit.fit_payment_activity(stats).payments_per_sec
    assert abs(fit.activity.base_per_sec * p_send - payments_per_sec) < 1e-18


def test_fallback_activity_keeps_the_payment_rate():
    """A cluster with no per-unit counts borrows the overall activity. The
    borrowed base rate is a wake-up rate, already divided by the overall send
    probability, so it must be turned back into a payment rate before this
    cluster's own send probability is applied; otherwise it is divided twice."""
    overall_fit = population_fit.fit_population(synthetic_stats(1, 16.0))
    small = synthetic_stats(2, 16.0)
    small.samples["tx_count_per_address"] = np.array([])
    small.shares["tx_type_mix"] = {"plain": 0.5, "script": 0.5, "stake": 0.0, "pool": 0.0}
    fit = population_fit.fit_population(small, overall_fit)
    payments = fit.activity.base_per_sec * fit.activity.send_probability
    want = overall_fit.activity.base_per_sec * overall_fit.activity.send_probability
    assert fit.activity.send_probability != overall_fit.activity.send_probability
    assert abs(payments / want - 1.0) < 1e-12
