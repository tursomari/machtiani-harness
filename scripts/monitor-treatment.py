#!/usr/bin/env python3
"""Monitor a Deep-SWE mct-orchestrator treatment run.

The script is intentionally artifact-oriented. It reads Pier logs and reward
files from the jobs directory, then inspects the copied mct-orchestrator,
mct-agent conversation, and shell-agent trajectory artifacts. When the Pier
containers are still alive it also samples the same files from /app inside the
matching Docker container.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


TASKS = [
    "ofetch-per-origin-circuit-breaker",
    "httpx-deterministic-cookie-store",
    "query-persist-restored-query-state",
    "vulture-persistent-analysis-cache",
    "arcane-drift-detection-baselines",
    "koota-entity-snapshot-rollback",
    "aiomonitor-task-snapshots-diff",
    "ts-pattern-match-each",
    "tomlkit-toml-table-converters",
    "vitest-duration-sharding",
    "task-task-graph-export",
    "abs-module-cache-flags",
]

RUNNER_ERROR_RE = re.compile(
    r"(Traceback \(most recent call last\)|RuntimeError|NonZeroAgentExitCodeError|sync failed after 10 retries|"
    r"hard blocker|panic:|(?:^|\s)ERROR:|exited with code [1-9][0-9]*|"
    r"GLIBC_[0-9.]+|forge: .*GLIBC|forge backend unavailable)",
    re.IGNORECASE,
)
PHASE_RE = re.compile(r"Phase\s+([1-4])|peer[_ -]review|hail[_ -]mary", re.IGNORECASE)
BENIGN_LOG_RE = re.compile(
    r"(resume load error: read trajectory: .* falling back to fresh run|"
    r"\[trajectory\] (cache usage|retry) listener error: trajectory/listener: decode event:)",
    re.IGNORECASE,
)
FORGE_FATAL_RE = re.compile(
    r"(GLIBC_[0-9.]+|forge: .*GLIBC|forge backend unavailable)",
    re.IGNORECASE,
)
FORGE_COMMAND_ERROR_RE = re.compile(
    r"\bmct-forge error:",
    re.IGNORECASE,
)


@dataclass
class RewardSummary:
    path: str = ""
    f2p: str = "-"
    p2p: str = "-"
    partial: str = "-"


@dataclass
class MetaSummary:
    files: int = 0
    lines: int = 0
    phases: list[str] = field(default_factory=list)
    sync_attempts: int = 0
    sync_retry_failures: int = 0
    sync_recovered: list[str] = field(default_factory=list)
    sync_failures: list[str] = field(default_factory=list)
    invocation_failures: list[str] = field(default_factory=list)
    crash_events: int = 0
    restore_events: int = 0


@dataclass
class ConversationSummary:
    files: int = 0
    sessions: list[str] = field(default_factory=list)
    statuses: list[str] = field(default_factory=list)
    suspended: int = 0
    messages: int = 0
    latest_mtime: float = 0.0


@dataclass
class ShellSummary:
    files: int = 0
    answer_events: int = 0
    error_events: int = 0
    forge_command_errors: int = 0
    latest_mtime: float = 0.0
    latest_path: str = ""


@dataclass
class TaskSummary:
    task: str
    trial_dir: str = ""
    containers: list[str] = field(default_factory=list)
    reward: RewardSummary = field(default_factory=RewardSummary)
    meta: MetaSummary = field(default_factory=MetaSummary)
    conversation: ConversationSummary = field(default_factory=ConversationSummary)
    shell: ShellSummary = field(default_factory=ShellSummary)
    runner_errors: list[str] = field(default_factory=list)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--work-dir",
        default=".data/mct-batch-subset",
        help="Batch work directory. Default: .data/mct-batch-subset",
    )
    parser.add_argument("--jobs-dir", help="Treatment jobs directory.")
    parser.add_argument("--job-name", help="Pier job name. Auto-detected when omitted.")
    parser.add_argument("--pier-log", help="Treatment pier.log path.")
    parser.add_argument(
        "--bench-dir",
        help="Optional persisted bench treatment directory to inspect in addition to jobs.",
    )
    parser.add_argument("--interval", type=float, default=30.0, help="Seconds between checks.")
    parser.add_argument("--once", action="store_true", help="Run one check and exit.")
    parser.add_argument("--json", action="store_true", help="Emit JSON instead of a table.")
    parser.add_argument(
        "--no-docker",
        action="store_true",
        help="Do not inspect live Docker containers.",
    )
    parser.add_argument(
        "--strict",
        action="store_true",
        help="Exit nonzero on runner, sync, invocation, or shell-agent errors.",
    )
    return parser.parse_args()


def read_text(path: Path, limit_bytes: int | None = None) -> str:
    try:
        if limit_bytes is None:
            return path.read_text(errors="replace")
        with path.open("rb") as fh:
            data = fh.read(limit_bytes)
        return data.decode(errors="replace")
    except OSError:
        return ""


def load_json(path: Path) -> Any | None:
    try:
        return json.loads(path.read_text(errors="replace"))
    except (OSError, json.JSONDecodeError):
        return None


def parse_json_lines_text(text: str) -> list[dict[str, Any]]:
    entries: list[dict[str, Any]] = []
    for line in text.splitlines():
        line = line.strip()
        if not line or not line.startswith("{"):
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            entries.append(value)
    return entries


def parse_json_document(text: str) -> Any | None:
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return None


def docker_ps() -> list[str]:
    try:
        out = subprocess.check_output(
            ["docker", "ps", "--format", "{{.Names}}"],
            text=True,
            stderr=subprocess.DEVNULL,
            timeout=5,
        )
    except (OSError, subprocess.SubprocessError):
        return []
    return [line.strip() for line in out.splitlines() if line.strip()]


def docker_exec(container: str, command: str) -> str:
    try:
        return subprocess.check_output(
            ["docker", "exec", container, "sh", "-lc", command],
            text=True,
            stderr=subprocess.DEVNULL,
            timeout=10,
        )
    except (OSError, subprocess.SubprocessError):
        return ""


def matching_containers(
    task: str,
    containers: list[str],
    trial_dir: Path | None = None,
    require_trial_suffix: bool = False,
) -> list[str]:
    if trial_dir is not None:
        trial_name = trial_dir.name.lower()
        if "__" in trial_name:
            suffix = trial_name.split("__", 1)[1]
            matched = [
                name
                for name in containers
                if name.endswith("-main-1") and suffix and suffix in name.lower()
            ]
            if matched:
                return matched
    if require_trial_suffix:
        return []
    task_prefix = task[:30].lower()
    return [
        name
        for name in containers
        if name.endswith("-main-1") and (task.lower() in name.lower() or task_prefix in name.lower())
    ]


def detect_job_name(jobs_dir: Path) -> str:
    if not jobs_dir.is_dir():
        return ""
    candidates = [p.name for p in jobs_dir.iterdir() if p.is_dir()]
    treatment = sorted([name for name in candidates if name.startswith("treatment-batch-")])
    if treatment:
        return treatment[-1]
    return sorted(candidates)[-1] if candidates else ""


def find_trial_dir(jobs_dir: Path, job_name: str, task: str) -> Path | None:
    job_dir = jobs_dir / job_name
    if not job_dir.is_dir():
        return None
    matches = sorted(job_dir.glob(f"{task}__*"))
    if matches:
        return matches[0]
    for prefix_len in range(len(task), 19, -1):
        matches = sorted(job_dir.glob(f"{task[:prefix_len]}__*"))
        if matches:
            return matches[0]
    return None


def reward_summary(task: str, trial_dir: Path | None, bench_dir: Path | None) -> RewardSummary:
    candidates: list[Path] = []
    if trial_dir is not None:
        candidates.append(trial_dir / "verifier" / "reward.json")
    if bench_dir is not None:
        candidates.append(bench_dir / task / "reward.json")
    for path in candidates:
        data = load_json(path)
        if not isinstance(data, dict):
            continue
        stats = data.get("reward_stats", {})
        if not isinstance(stats, dict):
            stats = {}
        if "reward_stats" in data:
            f2p = stats.get("f2p", {})
            p2p = stats.get("p2p", {})
            f2p_text = f"{f2p.get('correct', 0)}/{f2p.get('total', 0)}" if isinstance(f2p, dict) else "-"
            p2p_text = f"{p2p.get('correct', 0)}/{p2p.get('total', 0)}" if isinstance(p2p, dict) else "-"
            partial_text = str(stats.get("partial", "-"))
        else:
            f2p_text = f"{data.get('f2p_passed', 0)}/{data.get('f2p_total', 0)}"
            p2p_text = f"{data.get('p2p_passed', 0)}/{data.get('p2p_total', 0)}"
            partial_text = str(data.get("partial", "-"))
        return RewardSummary(
            path=str(path),
            f2p=f2p_text,
            p2p=p2p_text,
            partial=partial_text,
        )
    return RewardSummary()


def collect_files(task: str, trial_dir: Path | None, bench_dir: Path | None, patterns: list[str]) -> list[Path]:
    roots: list[Path] = []
    if trial_dir is not None:
        roots.extend([trial_dir / "agent", trial_dir / "agent" / "repo"])
    if bench_dir is not None:
        roots.extend([bench_dir / task / "agent", bench_dir / task / "agent" / "repo"])
    files: list[Path] = []
    seen: set[Path] = set()
    for root in roots:
        if not root.is_dir():
            continue
        for pattern in patterns:
            for path in root.glob(pattern):
                if path.is_file() and path not in seen:
                    seen.add(path)
                    files.append(path)
    return files


def summarize_meta(
    task: str,
    trial_dir: Path | None,
    bench_dir: Path | None,
    containers: list[str],
) -> MetaSummary:
    files = collect_files(
        task,
        trial_dir,
        bench_dir,
        [
            "meta-orchestrator/sessions/*/trajectory.jsonl",
            ".machtiani/meta-orchestrator/sessions/*/trajectory.jsonl",
        ],
    )
    live_files = 0
    texts = [read_text(path) for path in files]
    for container in containers:
        text = docker_exec(
            container,
            "find /app/.machtiani/meta-orchestrator/sessions -name trajectory.jsonl "
            "-maxdepth 3 -type f -exec cat {} + 2>/dev/null",
        )
        if text:
            live_files += 1
            texts.append(text)

    summary = MetaSummary(files=len(files) + live_files, phases=[])
    seen_phases: set[str] = set()
    pending_sync_failures: dict[str, int] = {}
    for text in texts:
        entries = parse_json_lines_text(text)
        summary.lines += len(entries)
        for entry in entries:
            typ = str(entry.get("type", ""))
            blob = json.dumps(entry, sort_keys=True)
            for match in PHASE_RE.finditer(blob):
                phase = match.group(0)
                phase = re.sub(r"\s+", " ", phase).strip()
                if phase not in seen_phases:
                    seen_phases.add(phase)
                    summary.phases.append(phase)
            if typ == "mct_sync_attempt":
                summary.sync_attempts += 1
                label = str(entry.get("label", "sync"))
                if int(entry.get("exit_code", 0) or 0) != 0:
                    summary.sync_retry_failures += 1
                    pending_sync_failures[label] = pending_sync_failures.get(label, 0) + 1
                elif label in pending_sync_failures:
                    summary.sync_recovered.append(f"{label}:{pending_sync_failures.pop(label)}")
            if typ == "mct_invocation" and int(entry.get("exit_code", 0) or 0) != 0:
                summary.invocation_failures.append(str(entry.get("session_id", "mct-agent")))
            if "crash" in typ:
                summary.crash_events += 1
            if typ == "runtime_state_restored":
                summary.restore_events += 1
    for label, count in pending_sync_failures.items():
        summary.sync_failures.append(f"{label}:{count}")
    return summary


def summarize_conversation(
    task: str,
    trial_dir: Path | None,
    bench_dir: Path | None,
    containers: list[str],
) -> ConversationSummary:
    files = collect_files(
        task,
        trial_dir,
        bench_dir,
        [
            "sessions/*/conversation.json",
            "sessions/*/artifacts/conversation.json",
            ".machtiani/sessions/*/conversation.json",
            ".machtiani/sessions/*/artifacts/conversation.json",
        ],
    )
    live_files = 0
    summary = ConversationSummary(files=len(files))
    payloads: list[tuple[dict[str, Any], float]] = []
    for path in files:
        data = load_json(path)
        if isinstance(data, dict):
            try:
                mtime = path.stat().st_mtime
            except OSError:
                mtime = 0.0
            payloads.append((data, mtime))

    for container in containers:
        text = docker_exec(
            container,
            "find /app/.machtiani/sessions -path '*/conversation.json' -type f "
            "-exec sh -c 'for f; do cat \"$f\"; printf \"\\n\"; done' sh {} + 2>/dev/null",
        )
        for line in text.splitlines():
            try:
                data = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(data, dict):
                live_files += 1
                payloads.append((data, time.time()))
    summary.files += live_files

    for data, mtime in payloads:
        session_id = str(data.get("session_id", ""))
        if session_id and session_id not in summary.sessions:
            summary.sessions.append(session_id)
        status = str(data.get("status", ""))
        if status and status not in summary.statuses:
            summary.statuses.append(status)
        if data.get("suspended_user_input") or status == "suspended_user_input":
            summary.suspended += 1
        messages = data.get("messages", [])
        if isinstance(messages, list):
            summary.messages += len(messages)
        summary.latest_mtime = max(summary.latest_mtime, mtime)
    return summary


def summarize_shell(
    task: str,
    trial_dir: Path | None,
    bench_dir: Path | None,
    containers: list[str],
) -> ShellSummary:
    files = collect_files(
        task,
        trial_dir,
        bench_dir,
        [
            "sessions/*/shell-agent/*/trajectory.json",
            ".machtiani/sessions/*/shell-agent/*/trajectory.json",
        ],
    )
    live_files = 0
    texts: list[tuple[str, str, float]] = []
    for path in files:
        try:
            mtime = path.stat().st_mtime
        except OSError:
            mtime = 0.0
        texts.append((str(path), read_text(path), mtime))

    for container in containers:
        text = docker_exec(
            container,
            "find /app/.machtiani/sessions -path '*/shell-agent/*/trajectory.json' -type f "
            "-exec cat {} + 2>/dev/null",
        )
        if text:
            live_files += 1
            texts.append((f"docker:{container}", text, time.time()))

    summary = ShellSummary(files=len(files) + live_files)
    for path, text, mtime in texts:
        document = parse_json_document(text)
        if isinstance(document, dict):
            messages = document.get("messages", [])
            if isinstance(messages, list):
                for message in messages:
                    if not isinstance(message, dict):
                        continue
                    content = str(message.get("content", ""))
                    if message.get("role") == "assistant" or "<answer-now>" in content:
                        summary.answer_events += 1
                    if (
                        "Traceback (most recent call last)" in content
                        or "NonZeroAgentExitCodeError" in content
                        or FORGE_FATAL_RE.search(content)
                    ):
                        summary.error_events += 1
                    if FORGE_COMMAND_ERROR_RE.search(content):
                        summary.forge_command_errors += 1
        entries = parse_json_lines_text(text)
        if not entries and '"type":"answer"' in text:
            summary.answer_events += text.count('"type":"answer"')
        for entry in entries:
            typ = str(entry.get("type", ""))
            level = str(entry.get("level", ""))
            if typ == "answer":
                summary.answer_events += 1
            if "error" in typ.lower() or level.lower() in {"error", "fatal"} or entry.get("error"):
                summary.error_events += 1
            content = str(entry.get("content", ""))
            if FORGE_COMMAND_ERROR_RE.search(content):
                summary.forge_command_errors += 1
        if mtime >= summary.latest_mtime:
            summary.latest_mtime = mtime
            summary.latest_path = path
    return summary


def pier_errors(pier_log: Path, task: str) -> list[str]:
    text = read_text(pier_log)
    if not text:
        return []
    lines = []
    task_prefix = task[:24]
    for line in text.splitlines():
        if RUNNER_ERROR_RE.search(line) and (
            task in line or task_prefix in line or "Traceback (most recent call last)" in line
        ):
            lines.append(line[-240:])
    return lines[-5:]


def agent_log_errors(trial_dir: Path | None, containers: list[str]) -> list[str]:
    texts: list[str] = []
    if trial_dir is not None:
        for rel in ("agent/mct-run.log", "agent/mct-sync.log"):
            text = read_text(trial_dir / rel)
            if text:
                texts.append(text)
    for container in containers:
        text = docker_exec(
            container,
            "for f in /logs/agent/mct-run.log /logs/agent/mct-sync.log; do "
            "test -f \"$f\" && cat \"$f\"; done 2>/dev/null",
        )
        if text:
            texts.append(text)

    lines: list[str] = []
    for text in texts:
        for line in text.splitlines():
            if line.startswith("MCT_SHELL_ACTION") and not FORGE_FATAL_RE.search(line):
                continue
            if RUNNER_ERROR_RE.search(line) and not BENIGN_LOG_RE.search(line):
                lines.append(line[-240:])
    return lines[-8:]


def check_once(args: argparse.Namespace) -> tuple[list[TaskSummary], dict[str, Any]]:
    work_dir = Path(args.work_dir)
    jobs_dir = Path(args.jobs_dir) if args.jobs_dir else work_dir / "treatment" / "jobs"
    pier_log = Path(args.pier_log) if args.pier_log else work_dir / "treatment" / "pier.log"
    job_name = args.job_name or detect_job_name(jobs_dir)
    bench_dir = Path(args.bench_dir) if args.bench_dir else None
    containers = [] if args.no_docker else docker_ps()

    summaries: list[TaskSummary] = []
    for task in TASKS:
        trial_dir = find_trial_dir(jobs_dir, job_name, task) if job_name else None
        live = matching_containers(task, containers, trial_dir, require_trial_suffix=bool(job_name))
        summary = TaskSummary(
            task=task,
            trial_dir=str(trial_dir or ""),
            containers=live,
            runner_errors=pier_errors(pier_log, task),
        )
        summary.runner_errors.extend(agent_log_errors(trial_dir, live))
        summary.reward = reward_summary(task, trial_dir, bench_dir)
        summary.meta = summarize_meta(task, trial_dir, bench_dir, live)
        summary.conversation = summarize_conversation(task, trial_dir, bench_dir, live)
        summary.shell = summarize_shell(task, trial_dir, bench_dir, live)
        summaries.append(summary)

    meta = {
        "work_dir": str(work_dir),
        "jobs_dir": str(jobs_dir),
        "job_name": job_name,
        "pier_log": str(pier_log),
        "bench_dir": str(bench_dir or ""),
        "running_pier": bool(
            subprocess.call(
                ["pgrep", "-f", "pier run"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            == 0
        ),
        "containers": containers,
    }
    return summaries, meta


def fmt_age(ts: float) -> str:
    if not ts:
        return "-"
    age = max(0, int(time.time() - ts))
    if age < 60:
        return f"{age}s"
    if age < 3600:
        return f"{age // 60}m"
    return f"{age // 3600}h{(age % 3600) // 60}m"


def print_table(summaries: list[TaskSummary], meta: dict[str, Any]) -> None:
    print(f"[monitor] {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}")
    print(
        f"jobs={meta['jobs_dir']} job={meta['job_name'] or '-'} "
        f"pier_running={meta['running_pier']} containers={len(meta['containers'])}"
    )
    print(
        f"{'task':34} {'reward':>9} {'meta':>15} {'sync':>12} "
        f"{'conv':>13} {'shell':>16} {'issues'}"
    )
    print("-" * 126)
    for s in summaries:
        issues: list[str] = []
        if s.runner_errors:
            issues.append(f"runner_errors={len(s.runner_errors)}")
        if s.meta.sync_failures:
            issues.append(f"sync_failures={','.join(s.meta.sync_failures[-3:])}")
        if s.meta.sync_recovered:
            issues.append(f"sync_recovered={','.join(s.meta.sync_recovered[-3:])}")
        if s.meta.invocation_failures:
            issues.append(f"mct_failures={len(s.meta.invocation_failures)}")
        if s.meta.crash_events:
            issues.append(f"crashes={s.meta.crash_events}")
        if s.conversation.suspended:
            issues.append(f"suspended={s.conversation.suspended}")
        if s.shell.error_events:
            issues.append(f"shell_errors={s.shell.error_events}")
        if s.shell.forge_command_errors:
            issues.append(f"forge_cmd_errors={s.shell.forge_command_errors}")
        reward = s.reward.f2p if s.reward.path else "-"
        meta_cell = f"{s.meta.files}f/{s.meta.lines}l/{len(s.meta.phases)}p"
        sync_cell = f"{s.meta.sync_attempts}/{len(s.meta.sync_failures)}"
        conv_cell = f"{s.conversation.files}f/{s.conversation.messages}m/{fmt_age(s.conversation.latest_mtime)}"
        shell_cell = f"{s.shell.files}f/{s.shell.answer_events}a/{fmt_age(s.shell.latest_mtime)}"
        print(
            f"{s.task[:34]:34} {reward:>9} {meta_cell:>15} {sync_cell:>12} "
            f"{conv_cell:>13} {shell_cell:>16} {'; '.join(issues)}"
        )
    reward_count = sum(1 for s in summaries if s.reward.path)
    sync_failures = sum(len(s.meta.sync_failures) for s in summaries)
    sync_retries = sum(s.meta.sync_retry_failures for s in summaries)
    runner_errors = sum(len(s.runner_errors) for s in summaries)
    shell_errors = sum(s.shell.error_events for s in summaries)
    forge_command_errors = sum(s.shell.forge_command_errors for s in summaries)
    print(
        f"\nsummary: rewards={reward_count}/{len(summaries)} "
        f"sync_failures={sync_failures} sync_retries={sync_retries} runner_errors={runner_errors} "
        f"shell_errors={shell_errors} forge_cmd_errors={forge_command_errors}"
    )


def has_strict_failure(summaries: list[TaskSummary]) -> bool:
    for summary in summaries:
        if summary.runner_errors:
            return True
        if summary.meta.sync_failures or summary.meta.invocation_failures or summary.meta.crash_events:
            return True
        if summary.shell.error_events:
            return True
    return False


def to_jsonable(summaries: list[TaskSummary], meta: dict[str, Any]) -> dict[str, Any]:
    return {
        "meta": meta,
        "tasks": [
            {
                "task": s.task,
                "trial_dir": s.trial_dir,
                "containers": s.containers,
                "reward": s.reward.__dict__,
                "meta_orchestrator": s.meta.__dict__,
                "conversation": s.conversation.__dict__,
                "shell_agent": s.shell.__dict__,
                "runner_errors": s.runner_errors,
            }
            for s in summaries
        ],
    }


def main() -> int:
    args = parse_args()
    exit_code = 0
    while True:
        summaries, meta = check_once(args)
        if args.json:
            print(json.dumps(to_jsonable(summaries, meta), indent=2, sort_keys=True))
        else:
            print_table(summaries, meta)
        if args.strict and has_strict_failure(summaries):
            exit_code = 2
        if args.once:
            return exit_code
        sys.stdout.flush()
        time.sleep(args.interval)


if __name__ == "__main__":
    raise SystemExit(main())
