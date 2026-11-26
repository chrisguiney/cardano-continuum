"""Render a grade as a Markdown report. Pure text building."""

from __future__ import annotations

from dataclasses import dataclass

from compare import Bounds, Row


@dataclass(frozen=True)
class ClusterGrade:
    """The comparison rows of one cluster of a run."""

    rows: list[Row]
    # The cluster's weight in the profile file, when the target records it.
    share: float | None
    # How many of the run's actors used this cluster's profile.
    actors: int


def table(rows: list[Row]) -> list[str]:
    """A Markdown table of comparison rows with a pass/fail/skip count."""
    lines = ["| statistic | verdict | detail |", "|---|---|---|"]
    lines += [f"| {row.name} | {row.status.upper()} | {row.detail()} |" for row in rows]
    passed = sum(row.status == "pass" for row in rows)
    failed = sum(row.status == "fail" for row in rows)
    lines += ["", f"{passed} pass, {failed} fail, {len(rows) - passed - failed} skipped."]
    return lines


def _cluster_section(name: str, grade: ClusterGrade) -> list[str]:
    share = f", share {grade.share:.1%}" if grade.share is not None else ""
    return ["", f"## Cluster {name}", "", f"{grade.actors} actors{share}.", ""] + table(grade.rows)


def render(
    run: str,
    target: str,
    bounds: Bounds,
    overall: list[Row],
    idle_fraction: float | None = None,
    clusters: dict[str, ClusterGrade] | None = None,
) -> str:
    """The whole report: the run as one population, then each cluster.

    run and target are the paths shown in the first line. idle_fraction is
    the share of the engine's decisions that did nothing, shown when known.
    """
    lines = [
        "# Calibration grade", "",
        f"Run: `{run}`; target: `{target}`; "
        f"KS bound D <= {bounds.ks}, TV <= {bounds.share}, fraction diff <= {bounds.fraction}.", "",
        "## Overall", "",
    ]
    lines += table(overall)
    if idle_fraction is not None:
        lines += ["", f"Idle wake-ups (IntentChosen=idle plus Tick) are "
                  f"{idle_fraction:.1%} of decision records."]
    for name, grade in (clusters or {}).items():
        lines += _cluster_section(name, grade)
    return "\n".join(lines) + "\n"
