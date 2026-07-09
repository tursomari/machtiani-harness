import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from mct_pier_adapter.mct_agent import (
    PROTECTED_RUNTIME_GIT_PATHS,
    artifact_preservation_commands,
    filtered_git_stage_command,
    protected_runtime_exclude_command,
)


class GitStagingCommandTest(unittest.TestCase):
    def run_git(self, cwd, *args):
        return subprocess.run(
            ["git", *args],
            cwd=cwd,
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def test_staging_excludes_runtime_artifacts(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.run_git(tmp, "init")
            self.run_git(tmp, "config", "user.email", "test@example.com")
            self.run_git(tmp, "config", "user.name", "test")

            with open(os.path.join(tmp, "src.txt"), "w", encoding="utf-8") as f:
                f.write("before\n")
            self.run_git(tmp, "add", "src.txt")
            self.run_git(tmp, "commit", "-m", "base")

            with open(os.path.join(tmp, "src.txt"), "w", encoding="utf-8") as f:
                f.write("after\n")
            with open(os.path.join(tmp, "new.txt"), "w", encoding="utf-8") as f:
                f.write("new\n")

            os.makedirs(os.path.join(tmp, ".machtiani", "sessions"), exist_ok=True)
            with open(os.path.join(tmp, ".machtiani", "config.toml"), "w", encoding="utf-8") as f:
                f.write("config = true\n")
            with open(os.path.join(tmp, ".machtiani", "sessions", "state"), "w", encoding="utf-8") as f:
                f.write("session\n")
            for path in PROTECTED_RUNTIME_GIT_PATHS:
                if path == ".machtiani":
                    continue
                with open(os.path.join(tmp, path), "w", encoding="utf-8") as f:
                    f.write("runtime\n")

            subprocess.run(
                protected_runtime_exclude_command(),
                cwd=tmp,
                shell=True,
                check=True,
                executable="/bin/bash",
            )
            subprocess.run(
                filtered_git_stage_command(),
                cwd=tmp,
                shell=True,
                check=True,
                executable="/bin/bash",
            )

            staged = self.run_git(tmp, "diff", "--cached", "--name-only").stdout.splitlines()
            self.assertEqual(staged, ["new.txt", "src.txt"])

            with open(os.path.join(tmp, ".git", "info", "exclude"), encoding="utf-8") as f:
                exclude = f.read()
            self.assertIn(".machtiani/", exclude)
            self.assertIn("instruction.md", exclude)

            for path in PROTECTED_RUNTIME_GIT_PATHS:
                self.assertTrue(os.path.exists(os.path.join(tmp, path)))


class ArtifactPreservationCommandTest(unittest.TestCase):
    def test_preservation_commands_include_runtime_and_git_diagnostics(self):
        commands = "\n".join(artifact_preservation_commands())

        for expected in (
            "/app/.machtiani/sessions",
            "/logs/agent/sessions",
            "/app/.machtiani/meta-orchestrator",
            "/logs/agent/meta-orchestrator",
            "git status --short",
            "/logs/agent/repo/git-status.txt",
            "git diff > /logs/agent/repo/git-diff.patch",
            "git diff --cached > /logs/agent/repo/git-diff-cached.patch",
            "git log --oneline -n 20",
            "/tmp/mct-agent",
            "/app/.machtiani/tmp-data",
            "/logs/agent/tmp-data",
        ):
            self.assertIn(expected, commands)

        self.assertNotIn("git add", commands)
        self.assertNotIn("git commit", commands)


if __name__ == "__main__":
    unittest.main()
