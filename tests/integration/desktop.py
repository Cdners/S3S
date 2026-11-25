"""Native Wails UI + Linux Secret Service + loopback S3 emulator."""
import hashlib
import json
import os
from pathlib import Path
import secrets
import sqlite3
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


def blank_secret(catalog, label):
    entry = lookup(catalog[label], 'password text')
    # WebKitGTK exposes an empty field's placeholder as its AT-SPI text.
    placeholder = next((attr.removeprefix('placeholder-text:') for attr in entry.getAttributes()
                        if attr.startswith('placeholder-text:')), '')
    if entry.queryText().getText(0, -1) not in ('', placeholder):
        raise AssertionError('Opening the account form revealed a populated credential field')
    return entry


def fill_keys(catalog, credentials):
    for label, field in [('credentials.access', 'accessKeyId'), ('credentials.secret', 'secretAccessKey')]:
        entry = blank_secret(catalog, label)
        entry.queryComponent().grabFocus()
        enter(credentials[field])


def check_keyring(root, name, expected):
    database = (root / 'XDG_CONFIG_HOME' / 'S3S' / 'workspace.sqlite3').as_uri()
    with sqlite3.connect(database + '?mode=ro', uri=True) as db:
        ids = db.execute('SELECT id FROM connections WHERE name = ?', (name,)).fetchall()
    if len(ids) != 1:
        raise AssertionError('Expected one saved account')
    # Read only the fixture-created item. Capture the result in memory so
    # credentials are never written to test output, logs or screenshots.
    value = subprocess.check_output(['secret-tool', 'lookup', 'service', 'io.github.cdners.S3S',
                                     'username', ids[0][0]])
    secret = json.loads(value)['Credentials']
    if any(secret.get(key) != expected[key] for key in expected):
        raise AssertionError('The scoped OS keyring item differs from the submitted fixture')


def check_storage(root, credential_sets):
    # SQLite can encode a text cell as UTF-8 or UTF-16. Inspect the application
    # database, sidecars and the entire persistent WebKit website-data profile.
    db_files = list((root / 'XDG_CONFIG_HOME' / 'S3S').glob('workspace.sqlite3*'))
    web = root / 'XDG_DATA_HOME' / 'io.github.cdners.s3s' / 'storage'
    if not db_files or not list(web.rglob('localstorage.sqlite3')):
        raise AssertionError('Missing application or WebKit storage in this test profile')
    needles = [value.encode(encoding) for credentials in credential_sets for value in credentials.values()
               for encoding in ('utf-8', 'utf-16-le', 'utf-16-be')]
    for path in db_files + [file for file in web.rglob('*') if file.is_file()]:
        data = path.read_bytes()
        if any(needle in data for needle in needles):
            raise AssertionError('Fixture credentials appeared outside the OS keyring')


def wait_completed_upload(en, object_name):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        cell = lookup(object_name, 'table cell')
        try:
            values = [node.queryText().getText(0, -1) for node in cell.parent
                      if 'Text' in node.get_interfaces()]
            if en['tasks.upload'] in values and en['state.completed'] in values:
                return
        except RuntimeError:
            pass  # The next queue poll may replace this row.
        time.sleep(.15)
    raise AssertionError('The uploaded object did not reach Completed in Task center')


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
            press(en['language.nativeName'], 'radio button')
            lookup(en['nav.connections'], 'heading')
            press(en['connections.add'])
            lookup(en['connections.add'], 'heading')
            enter(name)
            fill_keys(en, credentials)
            press(en['connections.save'])
            lookup(name, 'table cell')
            check_keyring(root, name, credentials)
            press(en['connections.actionEdit'].format(name=name))
            lookup(en['connections.edit'], 'heading')
            for label in ('credentials.access', 'credentials.secret', 'credentials.token'):
                blank_secret(en, label)
            # Save the existing account with empty secret fields. The same-scoped
            # keyring item must remain usable by the real backend after restart.
            press(en['connections.save'])
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
            press(zh['language.nativeName'], 'radio button')
            lookup(zh['nav.objects'], 'heading')
            lookup('ui-proof.txt', 'table cell')
            lookup(bucket, 'heading')
            press(en['language.nativeName'], 'radio button')
            lookup(en['nav.objects'], 'heading')
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
            # Save created a real entry in GTK's Recent list. Replace this
            # test file's bytes to distinguish the upload from the download.
            destination.write_bytes(hashlib.shake_256(b'S3S UI upload').digest((8 << 20) + 37))
            upload_key = 'ui-upload +%.bin'
            press(en['objects.upload'])
            lookup(en['objects.upload'], 'heading')
            enter(upload_key)
            press(en['upload.chooseFile'])
            lookup(en['native.openTitle'])
            file_row = lookup(destination.name, 'table cell').parent
            if not file_row.parent.querySelection().selectChild(file_row.getIndexInParent()):
                raise AssertionError('GTK could not select the source file')
            press('Open')
            visit(en, 'tasks')
            wait_completed_upload(en, bucket + '/' + upload_key)
            visit(en, 'connections')
            press(en['connections.add'])
            lookup(en['connections.add'], 'heading')
            tos_name = 'TOS setup fixture'
            enter(tos_name)
            lookup(en['connections.provider'], 'combo box').queryComponent().grabFocus()
            keys('Home', 'Down', 'Down', 'Down', 'Return')
            lookup(en['account.planned'])
            press(en['account.fillExample'])
            for label, expected in [('connections.region', 'cn-beijing'),
                                    ('connections.endpoint', 'https://tos-cn-beijing.volces.com')]:
                if lookup(en[label], 'entry').queryText().getText(0, -1) != expected:
                    raise AssertionError('The TOS example filled an unexpected account scope')
            tos_keys = {'accessKeyId': secrets.token_hex(10).upper(), 'secretAccessKey': secrets.token_hex(32)}
            fill_keys(en, tos_keys)
            press(en['connections.save'])
            lookup(tos_name, 'table cell')
            check_keyring(root, tos_name, tos_keys)
            visit(en, 'objects')
            select_end(en['objects.connection'])
            lookup(en['objects.unsupported'], 'heading')
        check_storage(root, (credentials, tos_keys))
    print('PASS single-form AWS/TOS accounts, protected edits and OS keyring, restart, storage scan, '
          'live language switch in open bucket, native file pickers, verified transfers and pending TOS adapter')


if __name__ == '__main__':
    verify()
