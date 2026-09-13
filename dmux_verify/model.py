from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class Stage:
    number: int
    title: str
    module: str
    objective: str = ""
    contract: str = ""
    acceptance_tests: list[str] = field(default_factory=list)
    study: list[str] = field(default_factory=list)
    questions: list[str] = field(default_factory=list)
    mode: str = "manual"
    checks: list[str] = field(default_factory=list)
    minimum_evidence: int = 0


@dataclass
class CheckResult:
    name: str
    passed: bool
    detail: str = ""
    duration_seconds: float = 0.0
    stdout: str = ""
    stderr: str = ""

    def as_dict(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "passed": self.passed,
            "detail": self.detail,
            "duration_seconds": round(self.duration_seconds, 4),
            "stdout": self.stdout,
            "stderr": self.stderr,
        }
