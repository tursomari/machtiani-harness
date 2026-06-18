import os
import shlex
from urllib.parse import urlparse

from pier.agents.installed.base import BaseInstalledAgent, with_prompt_template
from pier.agents.network import allowlist_from_urls
from pier.environments.base import BaseEnvironment
from pier.models.agent.context import AgentContext
from pier.models.agent.install import AgentInstallSpec, InstallStep
from pier.models.agent.network import NetworkAllowlist


class MctAgent(BaseInstalledAgent):
    """Pier agent adapter that runs mct-agent inside a task container.

    Post-run it commits all changes so the Pier verifier can capture
    the model patch via ``git diff base_commit HEAD``.
    """

    @staticmethod
    def name() -> str:
        return "mct-agent"

    def install_spec(self) -> AgentInstallSpec:
        return AgentInstallSpec(
            agent_name="mct-agent",
            steps=[
                InstallStep(
                    user="root",
                    run="apt-get update && apt-get install -y ripgrep rsync",
                ),
                InstallStep(
                    user="root",
                    run="cd /tmp && git clone --depth 1 https://github.com/tursomari/mchtiani.git mchtiani && cd mchtiani/agent && go build -o /usr/local/bin/mct-agent ./cmd/mct-agent && rm -rf /tmp/mchtiani",
                ),
            ],
            verification_command="mct-agent --help",
        )

    def network_allowlist(self) -> NetworkAllowlist:
        base_url = os.environ.get("TEST_BASE_URL", "")
        if base_url:
            return allowlist_from_urls([base_url])
        return NetworkAllowlist(domains=["openrouter.ai"])

    @with_prompt_template
    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        """Run mct-agent on the task, then commit all changes."""
        # Write the task instruction to a file.
        await self.exec_as_agent(
            environment,
            "python3 -c \"import pathlib, os; pathlib.Path('/app/instruction.md').write_text(os.environ['MCT_INSTRUCTION'])\"",
            env={"MCT_INSTRUCTION": instruction},
        )

        # Build config.toml from environment variables.
        test_model = os.environ.get("TEST_MODEL", "")
        test_base_url = os.environ.get("TEST_BASE_URL", "https://openrouter.ai/api/v1")
        test_api_key = os.environ.get("TEST_API_KEY", "")

        parsed = urlparse(test_base_url)
        hostname = parsed.hostname
        provider = hostname.split(".")[0] if hostname else "openrouter"

        config_toml = (
            'default_model = "deepswe"\n\n'
            f"[providers.{provider}]\n"
            f'base_url = "{test_base_url}"\n\n'
            '[models.deepswe]\n'
            f'provider = "{provider}"\n'
            f'model = "{test_model}"\n\n'
            '[models.deepswe.params]\n'
            'reasoning_effort = "xhigh"\n'
        )

        await self.exec_as_agent(
            environment,
            "python3 -c \"import pathlib, os; pathlib.Path('/app/.machtiani/config.toml').write_text(os.environ['MCT_CONFIG'])\"",
            env={"MCT_CONFIG": config_toml},
        )

        # Run mct-agent.
        provider_key = f"{provider}:{test_api_key}"
        cmd = f"mct-agent run -f /app/instruction.md --model deepswe --api-key {shlex.quote(provider_key)}"
        result = await self.exec_as_agent(environment, cmd, check=False)
        # Continue even if mct-agent exits non-zero; the verifier will judge.

        # Commit all changes so the Pier verifier can capture the model patch.
        await self.exec_as_agent(
            environment,
            'git config user.email agent@machtiani.com && git config user.name mct-agent',
            check=False,
        )
        await self.exec_as_agent(environment, "git add -A", check=False)
        await self.exec_as_agent(
            environment, 'git commit -m "fix" --allow-empty || true', check=False
        )

    def populate_context_post_run(self, context: AgentContext) -> None:
        """Minimal stub – no trajectory parsing needed for grading."""
        pass
