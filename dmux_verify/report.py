from __future__ import annotations

from pathlib import Path
from typing import Any

from .guide import load_stages
from .state import ProgressStore


def markdown_report(stages, store: ProgressStore) -> str:
    lines = ["# DurableMux Verification Report", ""]
    passed = sum(1 for n in stages if store.is_passed(n))
    lines.append(f"**Progress:** {passed}/{len(stages)} stages passed")
    lines.append("")
    lines.append("| Stage | Title | Mode | Status | Evidence |")
    lines.append("|---:|---|---|---|---:|")
    for number, stage in stages.items():
        item = store.stage(number)
        status = item.get("status", "locked" if number > 1 and not store.is_passed(number - 1) else "ready")
        lines.append(f"| {number} | {stage.title} | {stage.mode} | {status} | {len(item.get('evidence', []))} |")
    lines.append("")
    for number, stage in stages.items():
        item = store.stage(number)
        if not item.get("last_run") and not item.get("evidence"):
            continue
        lines.extend([f"## Stage {number}: {stage.title}", ""])
        run = item.get("last_run")
        if run:
            lines.append(f"Result: **{'PASS' if run.get('passed') else 'FAIL'}**")
            lines.append("")
            for check in run.get("checks", []):
                icon = "PASS" if check.get("passed") else "FAIL"
                lines.append(f"- **{icon}:** {check.get('name')} — {check.get('detail', '')}")
        evidence = item.get("evidence", [])
        if evidence:
            lines.append("")
            lines.append("Evidence:")
            for entry in evidence:
                lines.append(f"- {entry.get('path') or entry.get('command')} — {entry.get('note', '')}")
        lines.append("")
    return "\n".join(lines)
