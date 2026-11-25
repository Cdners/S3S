"""Native Wails UI + Linux Secret Service + loopback S3 emulator."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

from native import desktop, lookup, press, visit


FIXTURE = Path('/fixture')


def keys(*sequence):
    subprocess.run(['xdotool', 'key', '--clearmodifiers', *sequence], check=True)


def enter(value):
    # Input travels over stdin, including for the ephemeral test-only access keys.
    subprocess.run(['xdotool', 'type', '--clearmodifiers', '--file', '-'],
                   input=value, text=True, check=True)


def select_end(label):
    lookup(label, 'combo box').queryComponent().grabFocus()
    keys('End', 'Return')


def verify():
    zh, en = [json.loads((FIXTURE / f'{code}.json').read_text()) for code in ('zh-CN', 'en-US')]
    credentials = json.loads((FIXTURE / 'credentials.json').read_text())
    name, bucket = 'Isolated cloud', 's3s-fixture'
    with tempfile.TemporaryDirectory() as folder:
        root = Path(folder)
        # The desktop smoke wrapper also sets these to the respective profile paths.
        for variable in ('XDG_RUNTIME_DIR', 'XDG_DATA_HOME', 'XDG_CACHE_HOME', 'XDG_CONFIG_HOME'):
            path = root / variable
            path.mkdir(mode=0o700)
            os.environ[variable] = str(path)
        subprocess.run(['gnome-keyring-daemon', '--unlock', '--components=secrets'],
                       input=secrets.token_hex(32), text=True, stdout=subprocess.DEVNULL, check=True)
        subprocess.run(['gnome-keyring-daemon', '--start', '--components=secrets'],
                       stdout=subprocess.DEVNULL, check=True)
        with desktop(FIXTURE / 'S3S', root):
            lookup(zh['connections.empty'], 'heading')
            visit(zh, 'settings')
            select_end(zh['settings.language'])
            visit(en, 'connections')
            press(en['connections.add'])
            lookup(en['connections.add'], 'heading')
            enter(name)
            press(en['connections.save'])
            lookup(name, 'table cell')
            press(en['connections.actionCredentials'].format(name=name))
            lookup(en['credentials.title'].format(name=name), 'heading')
            enter(credentials['accessKeyId'])
            keys('Tab')
            enter(credentials['secretAccessKey'])
            press(en['credentials.save'])
            # Wait for the modal to close, then verify a real request before restart.
            visit(en, 'objects')
            select_end(en['objects.connection'])
            press(en['objects.loadBuckets'])
            lookup(bucket, 'button')
        with desktop(FIXTURE / 'S3S', root):
            lookup(name, 'table cell')
            visit(en, 'objects')
            select_end(en['objects.connection'])
            press(en['objects.loadBuckets'])
            press(bucket)
            lookup('ui-proof.txt', 'table cell')
            press(en['objects.download'])
            lookup(en['native.saveTitle'])
            destination = root / 'exact-bytes.txt'
            keys('ctrl+l')
            enter(str(destination))
            keys('Return')
            for _ in range(150):
                if destination.exists():
                    break
                time.sleep(.1)
            if destination.read_bytes() != b'S3S native browser fixture\n':
                raise AssertionError('Desktop download differed from fixture bytes')
            visit(en, 'tasks')
            lookup(en['state.completed'], 'table cell')
        for path in (root / 'XDG_CONFIG_HOME' / 'S3S').glob('workspace.sqlite3*'):
            data = path.read_bytes()
            if any(word.encode() in data for word in credentials.values()):
                raise AssertionError('A fixture credential appeared in SQLite')
    print('PASS native credentials, OS keyring after restart, buckets/objects, native Save dialog, download bytes and completed task')


if __name__ == '__main__':
    verify()
