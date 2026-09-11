#!/usr/bin/env python3
"""Real config-wizard PTY tests against a local model-host fixture; no provider calls."""
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import subprocess
import sys
import tempfile
import time
import tomllib
import unittest


def model_host_fixture():
    args = sys.argv[2:]
    path = Path(args[args.index('--profile') + 1])
    profile = json.loads(path.read_text())
    assert path.stat().st_mode & 0o077 == 0
    assert profile['driver'] == 'openai-codex-app-server'
    assert profile['provider'] == 'openai-codex'
    assert profile['authMethod'] == 'subscription'
    runtime = Path(profile['runtimeProfile'])
    runtime.mkdir(mode=0o700, parents=True, exist_ok=True)
    signed_in = runtime / 'signed-in'
    login = args[:2] == ['auth', 'login']
    request = None if login else json.loads(sys.stdin.readline())
    method = 'login' if login else request['method']
    with open(os.environ['CHATGPT_SMOKE_CALLS'], 'a') as calls:
        calls.write(json.dumps({'method': method, 'profile': str(path), 'args': args}) + '\n')
    if login:
        mode = args[args.index('--mode') + 1]
        print('Fixture sign-in mode: ' + mode, flush=True)
        print('Complete fixture sign-in [enter]: ', end='', flush=True)
        input()
        if os.environ.get('CHATGPT_SMOKE_LOGIN_FAIL'):
            return 1
        signed_in.write_text('fixture-auth-marker')
        print('Authentication completed for openai-codex.', flush=True)
        return 0
    result = {'v': 1, 'id': request['id']}
    if method == 'auth/status':
        result['result'] = {'authenticated': signed_in.exists(), 'method': 'subscription'}
    elif method == 'models/list':
        assert signed_in.exists(), 'model discovery must follow sign-in'
        models = [
            {'id': 'gpt-test-one', 'name': 'GPT Test One', 'reasoningEfforts': ['low', 'high']},
            {'id': 'gpt-test-two', 'name': 'GPT Test Two', 'reasoningEfforts': ['high']},
        ]
        mode = os.environ.get('CHATGPT_SMOKE_CATALOG', '')
        if mode == 'refresh':
            models.append({'id': 'gpt-test-new', 'name': 'GPT Test New', 'reasoningEfforts': []})
        if mode == 'empty':
            models = []
        if mode == 'error':
            result['error'] = {'code': 'AUTH_EXPIRED', 'message': 'private-upstream-detail'}
        elif mode == 'malformed':
            result['result'] = {'provider': 'wrong', 'models': models}
        else:
            result['result'] = {'provider': 'openai-codex', 'models': models}
    else:
        raise AssertionError('unexpected protocol method: ' + method)
    print(json.dumps(result), flush=True)
    return 0


class Terminal:
    def __init__(self, argv, env, cwd):
        self.master, slave = pty.openpty()
        self.process = subprocess.Popen(argv, stdin=slave, stdout=slave, stderr=slave, env=env, cwd=cwd, start_new_session=True)
        os.close(slave)
        self.text = ''
        self.offset = 0

    def drain(self, timeout=0.1):
        if select.select([self.master], [], [], timeout)[0]:
            try:
                data = os.read(self.master, 65536)
            except OSError:
                data = b''
            self.text += data.decode('utf-8', errors='replace')

    def expect(self, value):
        deadline = time.monotonic() + 15
        while value not in self.text[self.offset:]:
            self.drain()
            if time.monotonic() > deadline or self.process.poll() is not None:
                self.drain(0)
                raise AssertionError(f'Expected {value!r}; output:\n{self.text}')
        self.offset = self.text.index(value, self.offset) + len(value)

    def send(self, value):
        os.write(self.master, value.encode())

    def finish(self, expected=0):
        deadline = time.monotonic() + 15
        while self.process.poll() is None and time.monotonic() < deadline:
            self.drain()
        self.drain(0)
        if self.process.poll() != expected:
            raise AssertionError(f'Expected exit {expected}, got {self.process.poll()}; output:\n{self.text}')
        return self.text

    def close(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait()
        os.close(self.master)


class ChatGPTWizardTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='machtiani-chatgpt-wizard-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root / 'config.toml'
        self.calls = self.root / 'calls.jsonl'
        self.host = self.root / 'model-host'
        self.host.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' ' + shlex.quote(str(Path(__file__).resolve())) + ' --model-host-fixture "$@"\n')
        self.host.chmod(0o700)
        self.env = os.environ.copy()
        for key in list(self.env):
            if key.startswith(('MACHTIANI_', 'OPENAI_', 'CODEX_', 'CHATGPT_SMOKE_')):
                self.env.pop(key)
        self.env.update(HOME=str(self.root), XDG_CONFIG_HOME=str(self.root / '.config'),
                        MACHTIANI_MODEL_HOST_BIN=str(self.host), CHATGPT_SMOKE_CALLS=str(self.calls),
                        TERM='xterm-256color', NO_COLOR='1', OPENAI_API_KEY='fixture-api-key')
        self.cli('config', 'add', '--provider', 'openai', '--url', 'https://api.example.invalid/v1',
                 '--api-key-env', 'OPENAI_API_KEY', '--model', 'gpt-test-one', '--alias', 'gpt-test-one', '--no-interactive')
        self.original = self.config.read_bytes()

    def cli(self, *args, expected=0):
        result = subprocess.run([BINARY, *args, '--path', str(self.config)], env=self.env, cwd=self.root,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
        self.assertEqual(result.returncode, expected, result.stdout)
        return result.stdout

    def terminal(self, *args):
        terminal = Terminal([BINARY, 'config', 'add', '--path', str(self.config), *args], self.env, self.root)
        self.addCleanup(terminal.close)
        return terminal

    def begin_new(self, mode='browser'):
        terminal = self.terminal()
        terminal.expect('ChatGPT subscription')
        terminal.send('j\r')  # Existing API provider precedes the new subscription choice.
        terminal.expect('Device code (SSH or headless)')
        terminal.send(('j' if mode == 'device_code' else '') + '\r')
        terminal.expect('Fixture sign-in mode: ' + mode)
        terminal.expect('Complete fixture sign-in [enter]: ')
        terminal.send('\r')
        return terminal

    def complete(self, terminal, model_keys='', suggested='chatgpt-gpt-test-one', alias='', reason_keys='', save=True):
        terminal.expect('GPT Test Two (gpt-test-two)')
        terminal.send(model_keys + '\r')
        terminal.expect('Alias [' + suggested + ']')
        terminal.send(alias + '\r')
        terminal.expect('Choose an effort offered by this model or keep the provider default.')
        terminal.send(reason_keys + '\r')
        terminal.expect('Context length [')
        terminal.send('\r')
        terminal.expect('the default model? [y/N]: ')
        terminal.send('n\r')
        terminal.expect('Apply this change? [y/N]: ')
        terminal.send(('y' if save else 'n') + '\r')
        if save:
            terminal.expect('Add another provider or model? [y/N]: ')
            terminal.send('n\r')
        return terminal.finish()

    def install(self):
        terminal = self.begin_new()
        return self.complete(terminal, reason_keys='jj')

    def data(self):
        return tomllib.loads(self.config.read_text())

    def operations(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def profiles(self):
        return list((self.root / '.config/machtiani/harness-profiles').glob('chatgpt-*'))

    def test_browser_setup_and_api_model_coexist(self):
        output = self.install()
        data = self.data()
        self.assertEqual(data['default_model'], 'gpt-test-one')
        self.assertEqual(data['models']['gpt-test-one']['provider'], 'openai')
        self.assertEqual(data['models']['chatgpt-gpt-test-one']['provider'], 'chatgpt')
        self.assertEqual(data['models']['chatgpt-gpt-test-one']['model'], 'gpt-test-one')
        self.assertEqual(data['models']['chatgpt-gpt-test-one']['params']['reasoning_effort'], 'high')
        self.assertEqual(data['providers']['openai'], tomllib.loads(self.original.decode())['providers']['openai'])
        path = Path(data['providers']['chatgpt']['profile'])
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        profile = json.loads(path.read_text())
        self.assertEqual(profile['model'], 'gpt-test-one')
        self.assertNotIn('fixture-auth-marker', output + self.config.read_text() + path.read_text())
        self.assertNotIn('chatgpt-GPT Test One', output)
        self.assertEqual([op['method'] for op in self.operations()], ['auth/status', 'login', 'auth/status', 'models/list'])
        self.cli('config', 'check')

    def test_reuse_account_refresh_models_and_prefixed_collision(self):
        self.install()
        path = Path(self.data()['providers']['chatgpt']['profile'])
        before = path.read_bytes()
        terminal = self.terminal()
        terminal.expect('Existing: chatgpt')
        terminal.send('\r')
        self.complete(terminal, model_keys='j', suggested='chatgpt-gpt-test-two')
        self.assertEqual(path.read_bytes(), before)
        terminal = self.terminal('--provider', 'chatgpt')
        self.complete(terminal, suggested='chatgpt-gpt-test-one-2')
        self.env['CHATGPT_SMOKE_CATALOG'] = 'refresh'
        terminal = self.terminal('--provider', 'chatgpt')
        self.complete(terminal, model_keys='jj', suggested='chatgpt-gpt-test-new', alias='my-subscription-model')
        data = self.data()
        self.assertEqual(data['models']['chatgpt-gpt-test-two']['model'], 'gpt-test-two')
        self.assertEqual(data['models']['chatgpt-gpt-test-one-2']['model'], 'gpt-test-one')
        self.assertEqual(data['models']['my-subscription-model']['model'], 'gpt-test-new')
        self.assertEqual(sum(op['method'] == 'login' for op in self.operations()), 1)
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(len(self.profiles()), 1)

    def test_device_code_sign_in(self):
        self.complete(self.begin_new('device_code'))
        login = next(op for op in self.operations() if op['method'] == 'login')
        self.assertEqual(login['args'][-1], 'device_code')

    def test_cancel_new_model_selection_preserves_config(self):
        terminal = self.begin_new()
        terminal.expect('GPT Test Two (gpt-test-two)')
        terminal.send('\003')
        terminal.finish(1)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.profiles(), [])

    def test_decline_save_removes_only_new_profile(self):
        self.complete(self.begin_new(), save=False)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.profiles(), [])

    def test_cancel_existing_preserves_profile_and_account(self):
        self.install()
        before = self.config.read_bytes()
        path = Path(self.data()['providers']['chatgpt']['profile'])
        profile = path.read_bytes()
        terminal = self.terminal('--provider', 'chatgpt')
        terminal.expect('GPT Test Two (gpt-test-two)')
        terminal.send('\003')
        terminal.finish(1)
        self.assertEqual(self.config.read_bytes(), before)
        self.assertEqual(path.read_bytes(), profile)
        self.assertTrue((Path(json.loads(profile)['runtimeProfile']) / 'signed-in').exists())

    def test_failed_sign_in_does_not_save(self):
        self.env['CHATGPT_SMOKE_LOGIN_FAIL'] = '1'
        self.begin_new().finish(1)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.profiles(), [])

    def test_discovery_failures_do_not_save_or_print_upstream_detail(self):
        for mode in ['error', 'empty', 'malformed']:
            with self.subTest(mode=mode):
                self.env['CHATGPT_SMOKE_CATALOG'] = mode
                output = self.begin_new().finish(1)
                self.assertNotIn('private-upstream-detail', output)
                self.assertEqual(self.config.read_bytes(), self.original)
                self.assertEqual(self.profiles(), [])

    def test_duplicate_unavailable_and_unsupported_effort_rejected(self):
        self.install()
        before = self.config.read_bytes()
        for options, code in [(['--model', 'gpt-test-one', '--alias', 'gpt-test-one'], 1),
                              (['--model', 'missing', '--alias', 'missing'], 1),
                              (['--model', 'gpt-test-two', '--alias', 'bad-effort', '--reasoning', 'low'], 2)]:
            self.cli('config', 'add', '--provider', 'chatgpt', '--no-interactive', *options, expected=code)
            self.assertEqual(self.config.read_bytes(), before)

    def test_existing_provider_renamed_still_browses_subscription(self):
        self.install()
        self.cli('config', 'provider', 'rename', 'chatgpt', 'personal', '--no-interactive')
        terminal = self.terminal('--provider', 'personal')
        self.complete(terminal, model_keys='j', suggested='chatgpt-gpt-test-two')
        self.assertEqual(self.data()['models']['chatgpt-gpt-test-two']['provider'], 'personal')
        self.assertEqual(len(self.profiles()), 1)

    def test_noninteractive_new_account_does_not_prompt_or_save(self):
        self.cli('config', 'add', '--provider', 'chatgpt', '--model', 'gpt-test-one', '--no-interactive', expected=1)
        self.assertEqual(self.config.read_bytes(), self.original)
        self.assertEqual(self.profiles(), [])

    def test_missing_runtime_does_not_change_config(self):
        self.env['MACHTIANI_MODEL_HOST_BIN'] = str(self.root / 'missing-runtime')
        output = self.cli('config', 'add', '--provider', 'chatgpt', '--model', 'gpt-test-one', '--no-interactive', expected=1)
        self.assertIn('requires machtiani-model-host', output)
        self.assertEqual(self.config.read_bytes(), self.original)


if __name__ == '__main__':
    if sys.argv[1:] == ['--model-host-fixture'] or '--model-host-fixture' in sys.argv[1:]:
        sys.exit(model_host_fixture())
    BINARY = os.environ.get('MACHTIANI_SMOKE_AGENT', 'machtiani')
    unittest.main()
