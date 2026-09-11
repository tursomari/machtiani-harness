#!/usr/bin/env python3
"""Model role selection through the real CLI and PTY; no provider calls."""
import copy
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest

spec = importlib.util.spec_from_file_location('wizard', Path(__file__).with_name('config-chatgpt-wizard.py'))
wizard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(wizard)
BINARY = os.environ.get('MACHTIANI_SMOKE_AGENT', 'machtiani')
CONFIG = '''default_model = "planner"
shell_agent_model = "planner"
answer_model = "planner"
file_discovery_model = "planner"

[providers.api]
base_url = "https://api.example.invalid/v1"
api_key = "${FIXTURE_API_KEY}"
[providers.chatgpt]
transport = "model-host"
profile = "~/fixture-profile.json"
command = "must-not-run-model-host"
[models.planner]
provider = "api"
model = "fixture-model"
[models.chatgpt-shell]
provider = "chatgpt"
model = "fixture-model"
'''


class ModelSelectionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='machtiani-model-selection-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.config = self.root / 'config.toml'
        self.config.write_text(CONFIG)
        self.config.chmod(0o600)
        self.env = os.environ.copy()
        for key in list(self.env):
            if key.startswith('MACHTIANI_'):
                self.env.pop(key)
        self.env.update(HOME=str(self.root), XDG_CONFIG_HOME=str(self.root / '.config'),
                        TERM='xterm-256color', NO_COLOR='1', FIXTURE_API_KEY='fixture-key')

    def cli(self, *args, expected=0):
        result = subprocess.run([BINARY, 'config', 'model', *args], cwd=self.root,
                                env=self.env, stdin=subprocess.DEVNULL, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
        self.assertEqual(result.returncode, expected, result.stdout)
        return result.stdout

    def assert_selection(self, path, key='shell_agent_model'):
        expected = copy.deepcopy(tomllib.loads(CONFIG))
        expected[key] = 'chatgpt-shell'
        self.assertEqual(tomllib.loads(path.read_text()), expected)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_noninteractive_scopes_preserve_other_settings(self):
        global_config = self.root / '.machtiani/config.toml'
        global_config.parent.mkdir()
        global_config.write_text(CONFIG)
        self.env['MACHTIANI_CONFIG'] = str(self.config)
        self.cli('shell-agent', 'chatgpt-shell', '--global', '--no-interactive')
        self.assert_selection(global_config)
        self.assertEqual(self.config.read_text(), CONFIG)
        self.cli('shell-agent', 'chatgpt-shell', '--no-interactive')
        self.assert_selection(self.config)
        self.config.write_text(CONFIG)
        self.env['MACHTIANI_CONFIG'] = str(self.root / 'missing.toml')
        self.cli('shell-agent', 'chatgpt-shell', '--path', str(self.config), '--no-interactive')
        self.assert_selection(self.config)
        self.assertFalse((self.root / 'missing.toml').exists())

    def test_invalid_arguments_do_not_write(self):
        for args, code in [([], 2), (['missing'], 1), (['planner', 'chatgpt-shell'], 2),
                           (['planner', '--global'], 1)]:
            with self.subTest(args=args):
                self.cli('shell-agent', *args, '--path', str(self.config), '--no-interactive', expected=code)
                self.assertEqual(self.config.read_text(), CONFIG)
        self.cli('shell-agent', 'chatgpt-shell', '--path', str(self.config), expected=1)
        self.assertEqual(self.config.read_text(), CONFIG)

    def test_default_selection_still_preserves_shell_selection(self):
        self.cli('default', 'chatgpt-shell', '--path', str(self.config), '--no-interactive')
        self.assert_selection(self.config, 'default_model')

    def terminal(self):
        terminal = wizard.Terminal([BINARY, 'config', 'model', 'shell-agent',
                                    '--path', str(self.config)], self.env, self.root)
        self.addCleanup(terminal.close)
        terminal.expect('Shell-agent model')
        terminal.expect('chatgpt-shell')
        return terminal

    def test_interactive_selection_saves_after_confirmation(self):
        terminal = self.terminal()
        terminal.send('\r')  # Aliases sort alphabetically: chatgpt-shell, planner.
        terminal.expect('Apply this change? [y/N]: ')
        self.assertEqual(self.config.read_text(), CONFIG)
        terminal.send('y\r')
        terminal.finish()
        self.assert_selection(self.config)

    def test_interactive_decline_and_cancel_preserve_config(self):
        terminal = self.terminal()
        terminal.send('\r')
        terminal.expect('Apply this change? [y/N]: ')
        terminal.send('n\r')
        terminal.finish()
        self.assertEqual(self.config.read_text(), CONFIG)
        terminal = self.terminal()
        terminal.send('\x03')
        terminal.finish(expected=1)
        self.assertEqual(self.config.read_text(), CONFIG)


if __name__ == '__main__':
    unittest.main()
