#!/usr/bin/env python3
"""Real native setup + sync against a loopback provider; no real keys or inboxes."""
import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading

binary = str(Path(sys.argv[1]).resolve())
seen = []

class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_POST(self):
        data = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
        key = self.headers.get('Authorization', '')
        seen.append((data.get('model'), key))
        text = '\n'.join(str(m.get('content', '')) for m in data.get('messages', []))
        if 'BEGIN_RELEVANT_FILES[file-discovery]' in text and 'file_search' in text:
            reply = 'BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n'
        else:
            reply = '# Internal README\n\nConfiguration isolation fixture.\n'
        result = {'id': 'fixture', 'object': 'chat.completion', 'model': data.get('model'),
                  'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': reply}, 'finish_reason': 'stop'}],
                  'usage': {'prompt_tokens': 1, 'completion_tokens': 1, 'total_tokens': 2}}
        body = json.dumps(result).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

with tempfile.TemporaryDirectory(prefix='machtiani-config-isolation-') as tmp:
    home = Path(tmp)
    env = {'HOME': tmp, 'XDG_CONFIG_HOME': str(home / '.config'), 'PATH': os.environ['PATH'],
           'MACHTIANI_UPDATE_REEXEC': '1', 'GIT_CONFIG_NOSYSTEM': '1',
           'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_AUTHOR_NAME': 'Fixture',
           'GIT_AUTHOR_EMAIL': 'fixture@example.invalid', 'GIT_COMMITTER_NAME': 'Fixture',
           'GIT_COMMITTER_EMAIL': 'fixture@example.invalid'}
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Provider)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    def run(args, cwd=home, environment=env, succeeds=True):
        result = subprocess.run(args, cwd=cwd, env=environment, text=True, capture_output=True, timeout=60)
        if (result.returncode == 0) != succeeds:
            raise AssertionError(f'Unexpected result for {args[1:3]}: {result.stdout}\n{result.stderr}')
        return result
    try:
        personal = home / '.config/machtiani/config.toml'
        managed = home / '.config/dearmachine/machtiani/config.toml'
        for label, config in [('personal', personal), ('managed', managed)]:
            selected = {**env, **({'MACHTIANI_CONFIG': str(config)} if label == 'managed' else {})}
            run([binary, 'config', 'add', '--provider', 'fixture', '--url',
                 f'http://127.0.0.1:{server.server_port}/v1', '--api-key', label+'-fixture-key',
                 '--model', label, '--alias', 'fixture', '--no-interactive'], environment=selected)
            assert label+'-fixture-key' not in config.read_text()
            before = personal.read_bytes()
            repo = home / (label+'-repo')
            repo.mkdir()
            run(['git', 'init', '--initial-branch=main'], repo)
            (repo/'README.md').write_text('# Fixture\n')
            run(['git', 'add', 'README.md'], repo)
            run(['git', '-c', 'commit.gpgsign=false', 'commit', '-m', 'fixture'], repo)
            run([binary, 'init', '--no-interactive'], repo, selected)
            start = len(seen)
            run([binary, 'sync'], repo, selected)
            assert len(seen) > start, 'sync made no provider request'
            assert all(model == label and key == 'Bearer '+label+'-fixture-key' for model,key in seen[start:]), 'wrong configuration or key'
            assert personal.read_bytes() == before, 'personal config modified'
        # Selected private file is authoritative, even with a different key inherited.
        credentials = managed.parent/'credentials.env'
        variable = credentials.read_text().split('=', 1)[0]
        credentials.unlink()
        start = len(seen)
        result = run([binary, 'sync'], home/'managed-repo', {**env, 'MACHTIANI_CONFIG': str(managed), variable: 'unrelated-fixture'}, succeeds=False)
        assert len(seen) == start, 'missing credentials reached provider'
        assert 'credential' in result.stderr.lower(), 'missing credential diagnostic absent: '+result.stdout+result.stderr
        print('PASS: native setup and real sync select independent configs and private credentials; missing credentials fail before HTTP')
    finally:
        server.shutdown()
        server.server_close()
