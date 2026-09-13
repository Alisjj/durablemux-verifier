from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

from . import __version__
from .checks import run_builtin, run_custom_checks
from .config import load_config, write_default_config
from .guide import load_stages
from .report import markdown_report
from .runner import CheckContext
from .state import ProgressStore, utc_now
from .util import git_commit, sha256_file

PACKAGE_ROOT = Path(__file__).resolve().parent.parent
GUIDE_PATH = PACKAGE_ROOT / "resources" / "durablemux-codecrafters-guide.md"
POLICIES_PATH = PACKAGE_ROOT / "resources" / "stage_policies.json"


def project_paths(project: Path):
    root = project.resolve()
    meta = root / ".dmux-verifier"
    return root, meta, meta / "config.json", meta / "progress.json"


def load_all(project: Path):
    root, meta, config_path, progress_path = project_paths(project)
    config = load_config(config_path)
    stages = load_stages(GUIDE_PATH, POLICIES_PATH)
    store = ProgressStore(progress_path)
    return root, meta, config, stages, store


def cmd_init(args) -> int:
    root, meta, config_path, progress_path = project_paths(Path(args.project))
    root.mkdir(parents=True, exist_ok=True)
    if config_path.exists() and not args.force:
        print(f"Already initialised: {config_path}", file=sys.stderr)
        return 2
    write_default_config(config_path, args.binary)
    ProgressStore(progress_path).save()
    (meta / "evidence").mkdir(parents=True, exist_ok=True)
    print(f"Initialised DurableMux verifier in {meta}")
    print(f"Edit {config_path} if your CLI differs from the default contract.")
    return 0


def cmd_doctor(args) -> int:
    try:
        root, meta, config, stages, store = load_all(Path(args.project))
    except Exception as exc:
        print(f"FAIL: {exc}")
        return 1
    binary = Path(config["binary"])
    if not binary.is_absolute():
        binary = (root / binary).resolve()
    checks = [
        (platform.system() == "Linux", f"platform is Linux ({platform.system()})"),
        (sys.version_info >= (3, 10), f"Python version is {platform.python_version()}"),
        (binary.exists(), f"binary exists at {binary}"),
        (os.access(binary, os.X_OK), f"binary is executable: {binary}"),
        (shutil.which("go") is not None, "Go is available (needed for stage 37)"),
        (shutil.which("bash") is not None, "bash is available"),
        (shutil.which("stty") is not None, "stty is available"),
    ]
    failed = False
    for ok, text in checks:
        print(f"{'PASS' if ok else 'FAIL'}  {text}")
        failed = failed or not ok
    return 1 if failed else 0


def stage_status(number: int, store: ProgressStore) -> str:
    if store.is_passed(number):
        return "passed"
    if number > 1 and not store.is_passed(number - 1):
        return "locked"
    item = store.stage(number)
    return item.get("status", "ready")


def cmd_status(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    if args.json:
        payload = []
        for number, stage in stages.items():
            payload.append({
                "stage": number,
                "title": stage.title,
                "mode": stage.mode,
                "status": stage_status(number, store),
                "evidence": store.evidence_count(number),
            })
        print(json.dumps(payload, indent=2))
        return 0
    passed = sum(store.is_passed(n) for n in stages)
    print(f"DurableMux progress: {passed}/{len(stages)} stages passed\n")
    for number, stage in stages.items():
        status = stage_status(number, store)
        marker = {"passed":"✓", "ready":"→", "failed":"✗", "locked":"·"}.get(status, "·")
        print(f"{marker} {number:02d}  {status:7}  [{stage.mode:6}]  {stage.title}")
    return 0


def cmd_show(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    stage = stages.get(args.stage)
    if not stage:
        print(f"Unknown stage: {args.stage}", file=sys.stderr)
        return 2
    print(f"Stage {stage.number}: {stage.title}")
    print(f"{stage.module} | mode: {stage.mode} | status: {stage_status(stage.number, store)}")
    print(f"\nObjective\n{stage.objective}")
    print(f"\nContract\n{stage.contract}")
    if stage.acceptance_tests:
        print("\nAcceptance tests")
        for item in stage.acceptance_tests:
            print(f"- {item}")
    if stage.questions:
        print("\nQuestions to answer")
        for item in stage.questions:
            print(f"- {item}")
    print(f"\nRequired evidence: {stage.minimum_evidence}; currently recorded: {store.evidence_count(stage.number)}")
    return 0


def resolve_stage(value: str, stages, store: ProgressStore) -> int:
    if value == "next":
        for number in stages:
            if not store.is_passed(number):
                return number
        raise ValueError("All core stages are already passed")
    number = int(value)
    if number not in stages:
        raise ValueError(f"Unknown stage: {number}")
    return number


def cmd_verify(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    try:
        number = resolve_stage(args.stage, stages, store)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2
    stage = stages[number]
    if number > 1 and not store.is_passed(number - 1) and not args.force:
        print(f"Stage {number} is locked. Pass stage {number - 1} first, or use --force for investigation.", file=sys.stderr)
        return 2

    binary = Path(config["binary"])
    if not binary.is_absolute():
        binary = (root / binary).resolve()
    if not binary.exists():
        print(f"Binary not found: {binary}", file=sys.stderr)
        return 2

    print(f"Verifying stage {number}: {stage.title} [{stage.mode}]")
    results = []
    with CheckContext(root, config) as ctx:
        for group in stage.checks:
            results.extend(run_builtin(group, ctx))
        results.extend(run_custom_checks(number, ctx))

    for result in results:
        print(f"{'PASS' if result.passed else 'FAIL'}  {result.name}")
        if result.detail:
            print(f"      {result.detail}")
        if not result.passed and result.stderr.strip():
            print("      stderr: " + result.stderr.strip().replace("\n", " | ")[:500])

    checks_passed = all(r.passed for r in results)
    evidence_ok = store.evidence_count(number) >= stage.minimum_evidence
    approval_ok = stage.mode == "auto" or store.approved(number)
    if stage.mode in ("manual", "hybrid"):
        print(f"{'PASS' if evidence_ok else 'NEED'}  evidence {store.evidence_count(number)}/{stage.minimum_evidence}")
        print(f"{'PASS' if approval_ok else 'NEED'}  manual review approval")
    passed = checks_passed and evidence_ok and approval_ok

    payload = {
        "at": utc_now(),
        "passed": passed,
        "stage": number,
        "mode": stage.mode,
        "checks": [r.as_dict() for r in results],
        "evidence_count": store.evidence_count(number),
        "manual_approved": store.approved(number),
        "git_commit": git_commit(root),
        "binary_sha256": sha256_file(binary),
    }
    store.record_run(number, payload)
    print(f"\nStage {number}: {'PASSED' if passed else 'NOT PASSED'}")
    if stage.mode in ("manual", "hybrid") and not approval_ok:
        print(f"After reviewing the evidence, run: dmux-verify approve {number} --note \"...\"")
    return 0 if passed else 1


def cmd_evidence(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    if args.stage not in stages:
        print(f"Unknown stage: {args.stage}", file=sys.stderr)
        return 2
    evidence_dir = meta / "evidence" / f"stage-{args.stage:02d}"
    evidence_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    record = {"at": utc_now(), "note": args.note or ""}
    if args.file:
        source = Path(args.file).resolve()
        if not source.exists():
            print(f"Evidence file not found: {source}", file=sys.stderr)
            return 2
        target = evidence_dir / f"{stamp}-{source.name}"
        shutil.copy2(source, target)
        record.update({"type":"file", "path": str(target.relative_to(root))})
    elif args.command:
        proc = subprocess.run(["sh", "-lc", args.command], cwd=root, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        target = evidence_dir / f"{stamp}-command.txt"
        target.write_text(
            f"$ {args.command}\n\n[exit] {proc.returncode}\n\n[stdout]\n{proc.stdout}\n[stderr]\n{proc.stderr}",
            encoding="utf-8",
        )
        record.update({"type":"command", "command":args.command, "exit_code":proc.returncode, "path":str(target.relative_to(root))})
    else:
        print("Provide --file or --command", file=sys.stderr)
        return 2
    store.add_evidence(args.stage, record)
    print(f"Recorded evidence for stage {args.stage}: {record.get('path')}")
    return 0


def cmd_approve(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    stage = stages.get(args.stage)
    if not stage:
        print(f"Unknown stage: {args.stage}", file=sys.stderr)
        return 2
    if store.evidence_count(args.stage) < stage.minimum_evidence:
        print(f"Stage {args.stage} requires at least {stage.minimum_evidence} evidence item(s) before approval.", file=sys.stderr)
        return 2
    store.approve(args.stage, args.note)
    print(f"Approved manual evidence for stage {args.stage}. Run verify {args.stage} to finalise it.")
    return 0


def cmd_report(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    text = markdown_report(stages, store)
    output = Path(args.output) if args.output else meta / "report.md"
    if not output.is_absolute():
        output = root / output
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(text + "\n", encoding="utf-8")
    print(f"Wrote report to {output}")
    return 0


def cmd_reset(args) -> int:
    root, meta, config, stages, store = load_all(Path(args.project))
    if args.stage is None:
        print("Specify --stage N", file=sys.stderr)
        return 2
    store.data.get("stages", {}).pop(str(args.stage), None)
    store.save()
    print(f"Reset stage {args.stage}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="dmux-verify", description="Progress verifier for the DurableMux CodeCrafters-style challenge")
    parser.add_argument("--version", action="version", version=__version__)
    parser.add_argument("--project", default=".", help="DurableMux project root (default: current directory)")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("init", help="initialise verifier metadata in a project")
    p.add_argument("--binary", default="./dmux")
    p.add_argument("--force", action="store_true")
    p.set_defaults(func=cmd_init)

    p = sub.add_parser("doctor", help="check verifier prerequisites")
    p.set_defaults(func=cmd_doctor)

    p = sub.add_parser("status", help="show stage progress")
    p.add_argument("--json", action="store_true")
    p.set_defaults(func=cmd_status)

    p = sub.add_parser("show", help="show one stage contract")
    p.add_argument("stage", type=int)
    p.set_defaults(func=cmd_show)

    p = sub.add_parser("verify", help="verify a stage number or 'next'")
    p.add_argument("stage")
    p.add_argument("--force", action="store_true", help="run even when prerequisites are not passed")
    p.set_defaults(func=cmd_verify)

    p = sub.add_parser("evidence", help="record a file or command transcript as stage evidence")
    p.add_argument("stage", type=int)
    group = p.add_mutually_exclusive_group(required=True)
    group.add_argument("--file")
    group.add_argument("--command")
    p.add_argument("--note", default="")
    p.set_defaults(func=cmd_evidence)

    p = sub.add_parser("approve", help="approve reviewed evidence for a manual or hybrid stage")
    p.add_argument("stage", type=int)
    p.add_argument("--note", required=True)
    p.set_defaults(func=cmd_approve)

    p = sub.add_parser("report", help="write a Markdown progress report")
    p.add_argument("--output")
    p.set_defaults(func=cmd_report)

    p = sub.add_parser("reset", help="clear one stage result")
    p.add_argument("--stage", type=int)
    p.set_defaults(func=cmd_reset)
    return parser


def main(argv=None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)
