"""Transparent clinical rules.

These are reference screeners with a published definition, not trained models
and not diagnoses. A pickled weight file would be unauditable; a rule with a
version string can be recomputed from the committed inputs.
"""

from __future__ import annotations

from dataclasses import dataclass

from medgit.errors import PolicyError

RULE_ID = "qsofa-ref-1"


@dataclass(frozen=True)
class QSOFA:
    respiratory_rate: int
    systolic_bp: int
    altered_mentation: bool
    score: int
    band: str
    points: tuple[str, ...]
    rule: str = RULE_ID

    @property
    def narrative(self) -> str:
        detail = "; ".join(self.points) if self.points else "no qSOFA points"
        return (
            f"qSOFA {self.score}/3 ({self.band}). {detail}. "
            "Screening rule only — not a diagnosis of sepsis."
        )


def score_qsofa(respiratory_rate: int, systolic_bp: int, altered_mentation: bool) -> QSOFA:
    if isinstance(respiratory_rate, bool) or isinstance(systolic_bp, bool):
        raise PolicyError("vital signs must be integers")
    if not isinstance(respiratory_rate, int) or not isinstance(systolic_bp, int):
        raise PolicyError("vital signs must be integers")
    if not isinstance(altered_mentation, bool):
        raise PolicyError("altered_mentation must be true or false")
    if not 0 <= respiratory_rate <= 80 or not 20 <= systolic_bp <= 300:
        raise PolicyError("vital sign is outside the plausible range for this rule")
    points: list[str] = []
    score = 0
    if respiratory_rate >= 22:
        score += 1
        points.append(f"respiratory rate {respiratory_rate} >= 22")
    if systolic_bp <= 100:
        score += 1
        points.append(f"systolic BP {systolic_bp} <= 100")
    if altered_mentation:
        score += 1
        points.append("altered mentation")
    if score <= 1:
        band = "low"
    elif score == 2:
        band = "moderate"
    else:
        band = "high"
    return QSOFA(
        respiratory_rate=respiratory_rate,
        systolic_bp=systolic_bp,
        altered_mentation=altered_mentation,
        score=score,
        band=band,
        points=tuple(points),
    )
