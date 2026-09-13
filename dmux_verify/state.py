from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


class ProgressStore:
    def __init__(self, path: Path):
        self.path = path
        self.data: dict[str, Any] = {"version": 1, "stages": {}}
        if path.exists():
            self.data = json.loads(path.read_text(encoding="utf-8"))

    def save(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(".tmp")
        tmp.write_text(json.dumps(self.data, indent=2) + "\n", encoding="utf-8")
        tmp.replace(self.path)

    def stage(self, number: int) -> dict[str, Any]:
        return self.data.setdefault("stages", {}).setdefault(str(number), {})

    def is_passed(self, number: int) -> bool:
        return self.stage(number).get("status") == "passed"

    def record_run(self, number: int, payload: dict[str, Any]) -> None:
        item = self.stage(number)
        item["last_run"] = payload
        item.setdefault("history", []).append(payload)
        item["status"] = "passed" if payload.get("passed") else "failed"
        item["updated_at"] = utc_now()
        self.save()

    def add_evidence(self, number: int, evidence: dict[str, Any]) -> None:
        item = self.stage(number)
        item.setdefault("evidence", []).append(evidence)
        item["updated_at"] = utc_now()
        self.save()

    def approve(self, number: int, note: str) -> None:
        item = self.stage(number)
        item["manual_approval"] = {"approved": True, "note": note, "at": utc_now()}
        item["updated_at"] = utc_now()
        self.save()

    def evidence_count(self, number: int) -> int:
        return len(self.stage(number).get("evidence", []))

    def approved(self, number: int) -> bool:
        return bool(self.stage(number).get("manual_approval", {}).get("approved"))
