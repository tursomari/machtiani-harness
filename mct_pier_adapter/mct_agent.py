import os
import shlex
from urllib.parse import urlparse

from pier.agents.installed.base import BaseInstalledAgent, NonZeroAgentExitCodeError, with_prompt_template
from pier.environments.base import BaseEnvironment
from pier.models.agent.context import AgentContext
from pier.models.agent.install import AgentInstallSpec, InstallStep
from pier.models.agent.network import NetworkAllowlist


PROTECTED_RUNTIME_GIT_PATHS = (
    ".machtiani",
    "instruction.md",
    "review-instruction.md",
    "stdout.log",
    "stderr.log",
)


def protected_runtime_exclude_command() -> str:
    patterns = " ".join(
        shlex.quote(path + "/" if path == ".machtiani" else path)
        for path in PROTECTED_RUNTIME_GIT_PATHS
    )
    return (
        "mkdir -p .git/info && "
        f"for p in {patterns}; do "
        "grep -qxF \"$p\" .git/info/exclude 2>/dev/null || "
        "printf '%s\\n' \"$p\" >> .git/info/exclude; "
        "done"
    )


def filtered_git_stage_command() -> str:
    excludes = " ".join(shlex.quote(f":(exclude){path}") for path in PROTECTED_RUNTIME_GIT_PATHS)
    unstage_paths = " ".join(shlex.quote(path) for path in PROTECTED_RUNTIME_GIT_PATHS)
    return (
        f"(git add -u -- . {excludes} && "
        f"git ls-files --others --exclude-standard -z -- . {excludes} | "
        "xargs -0r git add --); "
        f"git reset -q -- {unstage_paths} 2>/dev/null || true"
    )


def artifact_preservation_commands() -> tuple[str, ...]:
    return (
        "mkdir -p /logs/agent && cp -r /app/.machtiani/sessions /logs/agent/sessions 2>/dev/null || true",
        "mkdir -p /logs/agent && cp -r /app/.machtiani/meta-orchestrator /logs/agent/meta-orchestrator 2>/dev/null || true",
        "mkdir -p /logs/agent/repo && "
        "(git status --short > /logs/agent/repo/git-status.txt 2>/dev/null || true) && "
        "(git diff > /logs/agent/repo/git-diff.patch 2>/dev/null || true) && "
        "(git diff --cached > /logs/agent/repo/git-diff-cached.patch 2>/dev/null || true) && "
        "(git log --oneline -n 20 > /logs/agent/repo/git-log.txt 2>/dev/null || true)",
        "mkdir -p /logs/agent/tmp-data && "
        "(cp -r /tmp/machtiani /logs/agent/tmp-data/ 2>/dev/null; "
        "cp -r /app/.machtiani/tmp-data /logs/agent/tmp-data/ 2>/dev/null; true)",
    )


class MctAgent(BaseInstalledAgent):
    """Pier agent adapter that runs machtiani inside a task container.

    Post-run it commits all changes so the Pier verifier can capture
    the model patch via ``git diff base_commit HEAD``.
    """

    @staticmethod
    def name() -> str:
        return "machtiani"

    def install_spec(self) -> AgentInstallSpec:
        return AgentInstallSpec(
            agent_name="machtiani",
            steps=[InstallStep(user="root", run="apt-get update && apt-get install -y ripgrep rsync")],
            verification_command="machtiani --help",
        )

    def network_allowlist(self) -> NetworkAllowlist:
        domains_env = os.environ.get("MACHTIANI_NETWORK_DOMAINS", "")
        if domains_env:
            domains = [d.strip() for d in domains_env.split(",") if d.strip()]
            return NetworkAllowlist(domains=domains)

        try:
            import tomllib
            with open(".machtiani/config.toml", "rb") as f:
                config = tomllib.load(f)
            domains = []
            for section, values in config.items():
                if section.startswith("providers.") and isinstance(values, dict):
                    base_url = values.get("base_url", "")
                    if base_url:
                        parsed = urlparse(base_url)
                        hostname = parsed.hostname
                        if hostname:
                            domains.append(hostname)
            if domains:
                return NetworkAllowlist(domains=domains)
        except (FileNotFoundError, OSError, ValueError, tomllib.TOMLDecodeError):
            pass

        return NetworkAllowlist(domains=["api.deepseek.com", "api.deepinfra.com", "openrouter.ai"])

    async def _preserve_artifacts(self, environment: BaseEnvironment) -> None:
        for command in artifact_preservation_commands():
            try:
                await self.exec_as_agent(environment, command)
            except NonZeroAgentExitCodeError:
                pass

    async def setup(self, environment: BaseEnvironment) -> None:
        await super().setup(environment)
        local_path = os.environ.get("MACHTIANI_BIN", "./agent/bin/machtiani")
        await environment.upload_file(local_path, "/usr/local/bin/machtiani")
        await self.exec_as_root(environment, "chmod +x /usr/local/bin/machtiani")

        mode = os.environ.get("MACHTIANI_MODE", "code-strong-forge")

        # Upload forge binary for forge-backed modes.
        forge_binary = os.path.expanduser(os.environ.get("MACHTIANI_FORGE_BINARY", "~/.local/bin/forge"))
        await environment.upload_file(forge_binary, "/usr/local/bin/forge")
        await self.exec_as_root(environment, "chmod +x /usr/local/bin/forge")
        await self.exec_as_root(environment, "/usr/local/bin/forge --version")

        # Upload mct-forge wrapper.
        forge_wrapper = os.path.expanduser(os.environ.get("MACHTIANI_FORGE_WRAPPER", "peripherals/mct-forge"))
        await environment.upload_file(forge_wrapper, "/usr/local/bin/mct-forge")
        await self.exec_as_root(environment, "chmod +x /usr/local/bin/mct-forge")

        # Upload meta-orchestrator binary (skip if not available, e.g., older control commits).
        meta_orch_path = os.path.expanduser(os.environ.get("MACHTIANI_META_ORCHESTRATOR_BINARY", "meta-orchestrator"))
        if os.path.isfile(meta_orch_path):
            await environment.upload_file(meta_orch_path, "/usr/local/bin/meta-orchestrator")
            await self.exec_as_root(environment, "chmod +x /usr/local/bin/meta-orchestrator")
            self._meta_orch_uploaded = True
        else:
            print(f"Warning: meta-orchestrator binary not found at {meta_orch_path}; skipping upload.")
            self._meta_orch_uploaded = False

        # Upload forge home directory.
        forge_home = os.path.expanduser(os.environ.get("MACHTIANI_FORGE_HOME", "~/.forge"))
        await self.exec_as_root(environment, "mkdir -p /root/.forge")
        await environment.upload_dir(forge_home, "/root/.forge/")

        # Upload the selected mode directory.
        await self.exec_as_agent(environment, f"mkdir -p /app/.machtiani/modes/{shlex.quote(mode)} /root/.forge")
        await environment.upload_dir(f"./.machtiani/modes/{mode}/", f"/app/.machtiani/modes/{mode}/")

    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        """Run machtiani on the task, then commit all changes."""

        mode = os.environ.get("MACHTIANI_MODE", "code-strong-forge")

        # Step 0: Create the /app/.machtiani/ directory.
        try:
            await self.exec_as_agent(environment, f"mkdir -p /app/.machtiani/modes/{shlex.quote(mode)} /root/.forge")
        except NonZeroAgentExitCodeError:
            raise RuntimeError("Failed to create /app/.machtiani/ directory")

        # Step 2: Upload the host config.toml.
        await environment.upload_file("./.machtiani/config.toml", "/app/.machtiani/config.toml")

        # Step 3: Upload the host mode directory.
        await environment.upload_dir(f"./.machtiani/modes/{mode}/", f"/app/.machtiani/modes/{mode}/")

        # Step 4: Write the instruction text to /app/instruction.md.
        await self.exec_as_agent(
            environment,
            "python3 -c \"import pathlib, os; "
            "pathlib.Path('/app/instruction.md').write_text(os.environ['MACHTIANI_INSTRUCTION'])\"",
            env={"MACHTIANI_INSTRUCTION": instruction},
        )

        # Keep repo-local runtime state available to the agent, but out of the
        # submitted patch Pier builds from the final commit.
        try:
            await self.exec_as_agent(environment, protected_runtime_exclude_command())
        except NonZeroAgentExitCodeError:
            pass

        # Step 5: Run machtiani sync with retries.
        import asyncio
        sync_model = os.environ.get("MACHTIANI_SYNC_MODEL", "deepseek-v4-pro")
        max_input_tokens = os.environ.get("MACHTIANI_MAX_INPUT_TOKENS", "800000")
        sync_log = "/logs/agent/mct-sync.log"
        sync_cmd = (
            f"mkdir -p /logs/agent && "
            f"(machtiani sync"
            f" --model {shlex.quote(sync_model)}"
            f" --max-input-tokens {shlex.quote(max_input_tokens)}"
            f") >> {shlex.quote(sync_log)} 2>&1"
        )
        for i in range(10):
            try:
                await self.exec_as_agent(environment, sync_cmd)
                break
            except NonZeroAgentExitCodeError:
                if i == 9:
                    raise RuntimeError("sync failed after 10 retries")
                await asyncio.sleep(2 ** i)

        # Step 6: Run machtiani run.
        model = os.environ.get("MACHTIANI_MODEL", "deepseek-v4-pro")
        shell_agent_model = os.environ.get("MACHTIANI_SHELL_AGENT_MODEL", "deepseek-v4-pro")

        use_meta = False
        try:
            await self.exec_as_agent(environment, "test -f /usr/local/bin/meta-orchestrator")
            use_meta = True
        except NonZeroAgentExitCodeError:
            pass
        binary = "meta-orchestrator" if use_meta else "machtiani run"
        if not use_meta:
            print("Meta-orchestrator not available; falling back to machtiani run")
        run_log = "/logs/agent/mct-run.log"
        run_cmd = (
            f"mkdir -p /logs/agent && ({binary}"
            f" --mode {shlex.quote(mode)}"
            f" --model {shlex.quote(model)}"
            f" --shell-agent-model {shlex.quote(shell_agent_model)}"
            f" --tag now"
            f" --persist-tmp-data"
            f" -f /app/instruction.md"
            f") >> {shlex.quote(run_log)} 2>&1"
        )
        try:
            await self.exec_as_agent(environment, run_cmd)
        except NonZeroAgentExitCodeError:
            pass
        finally:
            await self._preserve_artifacts(environment)

        # Step 7: Commit all changes so the Pier verifier can capture the model patch.
        try:
            await self.exec_as_agent(
                environment,
                'git config user.email agent@machtiani.com && git config user.name machtiani',
            )
        except NonZeroAgentExitCodeError:
            pass
        try:
            await self.exec_as_agent(
                environment,
                filtered_git_stage_command(),
            )
        except NonZeroAgentExitCodeError:
            pass
        try:
            await self.exec_as_agent(
                environment, 'git commit -m "fix" --allow-empty',
            )
        except NonZeroAgentExitCodeError:
            pass

        # Step 8: Copy diagnostics to host-visible logs directory after the
        # final commit path as well as after the long agent invocation.
        await self._preserve_artifacts(environment)

    def populate_context_post_run(self, context: AgentContext) -> None:
        """Minimal stub - no trajectory parsing needed for grading."""
        pass
