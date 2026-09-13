from __future__ import annotations

import json
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "dmux-verify-py"
FAKE = ROOT / "tests" / "fixtures" / "fake_dmux.py"


class VerifierTests(unittest.TestCase):
    def run_cli(self, project: Path, *args: str):
        return subprocess.run(
            [str(CLI), "--project", str(project), *args],
            cwd=ROOT,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=30,
        )

    def test_init_and_first_three_stages(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            init = self.run_cli(project, "init", "--binary", str(FAKE))
            self.assertEqual(init.returncode, 0, init.stderr)
            for stage in (1, 2, 3):
                result = self.run_cli(project, "verify", str(stage))
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            status = self.run_cli(project, "status", "--json")
            payload = json.loads(status.stdout)
            self.assertEqual(payload[0]["status"], "passed")
            self.assertEqual(payload[1]["status"], "passed")
            self.assertEqual(payload[2]["status"], "passed")
            self.assertEqual(payload[3]["status"], "ready")

    def test_locking(self):
        with tempfile.TemporaryDirectory() as td:
            project = Path(td)
            self.run_cli(project, "init", "--binary", str(FAKE))
            result = self.run_cli(project, "verify", "2")
            self.assertEqual(result.returncode, 2)
            self.assertIn("locked", result.stderr.lower())

    def test_guide_contains_38_core_stages(self):
        project = ROOT
        proc = subprocess.run(
            ["python3", "-c", (
                "from pathlib import Path; "
                "from dmux_verify.guide import load_stages; "
                "s=load_stages(Path('resources/durablemux-codecrafters-guide.md'),Path('resources/stage_policies.json')); "
                "print(len(s), min(s), max(s))"
            )],
            cwd=ROOT,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=10,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertEqual(proc.stdout.strip(), "38 1 38")


if __name__ == "__main__":
    unittest.main()
