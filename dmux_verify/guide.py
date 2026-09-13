from __future__ import annotations

import json
import re
from pathlib import Path

from .model import Stage

_STAGE_RE = re.compile(r"^## Stage (\d+): (.+)$")
_EXTENSION_RE = re.compile(r"^## Extension (\d+): (.+)$")
_MODULE_RE = re.compile(r"^# Module ([A-Z]) — (.+)$")
_SECTION_RE = re.compile(r"^### (Objective|Contract|Acceptance tests|Study before implementation|Questions to answer)$")


def _clean_list(lines: list[str]) -> list[str]:
    out: list[str] = []
    for line in lines:
        text = line.strip()
        if text.startswith("- "):
            out.append(text[2:].strip())
        elif re.match(r"^\d+\.\s+", text):
            out.append(re.sub(r"^\d+\.\s+", "", text))
    return out


def _clean_text(lines: list[str]) -> str:
    kept: list[str] = []
    in_fence = False
    for line in lines:
        if line.strip().startswith("```"):
            in_fence = not in_fence
            continue
        if in_fence:
            kept.append(line.rstrip())
        elif line.strip() and line.strip() != "---":
            kept.append(line.strip())
    return "\n".join(kept).strip()


def load_stages(guide_path: Path, policies_path: Path) -> dict[int, Stage]:
    policies = json.loads(policies_path.read_text(encoding="utf-8"))
    lines = guide_path.read_text(encoding="utf-8").splitlines()
    stages: dict[int, Stage] = {}
    current_module = ""
    i = 0
    while i < len(lines):
        line = lines[i]
        module_match = _MODULE_RE.match(line)
        if module_match:
            current_module = f"Module {module_match.group(1)} — {module_match.group(2)}"
            i += 1
            continue
        match = _STAGE_RE.match(line)
        if not match:
            i += 1
            continue
        number = int(match.group(1))
        title = match.group(2)
        i += 1
        sections: dict[str, list[str]] = {}
        current_section: str | None = None
        while i < len(lines) and not _STAGE_RE.match(lines[i]) and not _EXTENSION_RE.match(lines[i]) and not _MODULE_RE.match(lines[i]):
            sec = _SECTION_RE.match(lines[i])
            if sec:
                current_section = sec.group(1)
                sections[current_section] = []
            elif current_section is not None:
                sections[current_section].append(lines[i])
            i += 1
        policy = policies.get(str(number), {})
        stages[number] = Stage(
            number=number,
            title=title,
            module=current_module,
            objective=_clean_text(sections.get("Objective", [])),
            contract=_clean_text(sections.get("Contract", [])),
            acceptance_tests=_clean_list(sections.get("Acceptance tests", [])),
            study=_clean_list(sections.get("Study before implementation", [])),
            questions=_clean_list(sections.get("Questions to answer", [])),
            mode=policy.get("mode", "manual"),
            checks=policy.get("checks", []),
            minimum_evidence=int(policy.get("minimum_evidence", 0)),
        )
    return stages
