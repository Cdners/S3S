"""Native Wails UI + Linux Secret Service + loopback S3 emulator."""
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import sqlite3
import subprocess
import tempfile
import time

from native import descendants, desktop, lookup, press, visit
import pyatspi


FIXTURE = Path('/fixture')


def keys(*sequence):
    subprocess.run(['xdotool', 'key', '--clearmodifiers', *sequence], check=True)


def enter(value):
    # Input travels over stdin, including for the ephemeral test-only access keys.
    subprocess.run(['xdotool', 'type', '--clearmodifiers', '--file', '-'],
                   input=value, text=True, check=True)


def fill_field(entry, value, protected=False):
    # An AT-SPI focus change and the X input event can arrive on different
    # WebKit event-loop turns. Verify before proceeding to the next field.
    for _ in range(3):
        entry.queryComponent().grabFocus()
        keys('ctrl+a')
        enter(value)
        text = entry.queryText()
        if (text.characterCount == len(value) if protected else text.getText(0, -1) == value):
            return
        time.sleep(.1)
    raise AssertionError('An input did not receive the expected fixture value')


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
        fill_field(entry, credentials[field], protected=True)


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


def wait_probe(en, scope):
    message = re.escape(en['probe.passed'].format(scope=scope, ms='TIMING')).replace('TIMING', r'[0-9,]+')
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        try:
            for node in descendants(pyatspi.Registry.getDesktop(0)):
                if node.name and re.fullmatch(message, node.name):
                    return
                if 'Text' in node.get_interfaces() and re.fullmatch(message, node.queryText().getText(0, -1)):
                    return
        except (RuntimeError, LookupError):
            pass
        time.sleep(.1)
    raise AssertionError(f'Missing successful read probe for {scope}')


def verify_clipboard(expected):
    # Read the X11 clipboard through a separate GTK client, independent of
    # both the WebView's success notice and focus/keyboard timing.
    import gi
    gi.require_version('Gtk', '4.0')
    gi.require_version('Gdk', '4.0')
    from gi.repository import Gdk, GLib, Gtk
    Gtk.init()
    clipboard = Gdk.Display.get_default().get_clipboard()
    loop = GLib.MainLoop()
    result = []

    def finished(source, task):
        result.append(source.read_text_finish(task))
        loop.quit()

    clipboard.read_text_async(None, finished)
    timeout = GLib.timeout_add_seconds(4, loop.quit)
    loop.run()
    if result:
        GLib.source_remove(timeout)
    if result != [expected]:
        raise AssertionError('Native clipboard round trip changed the object key')


def check_regional_workspace(root, en, provider, name, region, endpoint, bucket, credentials):
    visit(en, 'connections')
    press(en['connections.add'])
    lookup(en['connections.add'], 'heading')
    fill_field(lookup(en['connections.name'], 'entry'), name)
    lookup(en['connections.provider'], 'combo box').queryComponent().grabFocus()
    keys('Home', *(['Down'] * {'tos': 3, 'oss': 2}[provider]), 'Return')
    lookup(en[f'account.guide.{provider}.origin'])
    press(en['account.fillExample'])
    for field, expected in [('connections.region', region), ('connections.endpoint', endpoint)]:
        if lookup(en[field], 'entry').queryText().getText(0, -1) != expected:
            raise AssertionError('The regional example filled an unexpected account scope')
    fill_keys(en, credentials)
    press(en['connections.save'])
    lookup(en['connections.actionEdit'].format(name=name), 'button')
    check_keyring(root, name, credentials)
    press(en['connections.testNamed'].format(name=name))
    if provider == 'tos':
        lookup(en['error.providerCode'].format(value='AccessDenied'))
        lookup(en['error.requestId'].format(value='isolated-tos-request'))
    else:
        wait_probe(en, en['probe.scopeAccount'].format(name=name))
    press(en['connections.connectNamed'].format(name=name))
    lookup(en['nav.objects'], 'heading')
    if provider == 'tos':
        lookup(en['error.requestId'].format(value='isolated-tos-request'))
        lookup(en['objects.bucketName'], 'entry').queryComponent().grabFocus()
        enter(bucket)
    else:
        press(bucket)
    press(en['probe.testBucket'])
    wait_probe(en, en['probe.scopeBucket'].format(bucket=bucket))
    if provider == 'tos':
        press(en['objects.openBucket'])
    key = '项目 +%2F.txt'
    lookup(key, 'table cell')
    visit(en, 'tasks')
    visit(en, 'objects')
    lookup(bucket, 'heading')
    lookup(key, 'table cell')
    press(en['objects.details'])
    lookup(en['objects.details'], 'heading')
    body = b'Object metadata from the isolated provider fixture.\n'
    etag = '"' + hashlib.md5(body, usedforsecurity=False).hexdigest() + '"'
    lookup(etag)
    press(en['objects.copyKey'])
    lookup(en['objects.copied'])
    verify_clipboard(key)
    keys('Escape')


def bandwidth(en, save):
    visit(en, 'settings')
    if save:
        mode = lookup(en['bandwidth.mode'], 'combo box')
        mode.queryComponent().grabFocus()
        keys('Home', 'Down', 'Down', 'Return')
    for field, number in [('bandwidth.upload', '8192.5'), ('bandwidth.download', '0')]:
        control = lookup(en[field], 'entry')
        if save:
            fill_field(control, number)
        elif control.queryText().getText(0, -1) != number:
            raise AssertionError('The native speed setting did not survive process restart')
    if save:
        press(en['bandwidth.apply'])
        lookup(en['bandwidth.saved'])


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
            fill_field(lookup(en['connections.name'], 'entry'), name)
            fill_keys(en, credentials)
            press(en['connections.save'])
            lookup(en['connections.actionEdit'].format(name=name), 'button')
            check_keyring(root, name, credentials)
            press(en['connections.actionEdit'].format(name=name))
            lookup(en['connections.edit'], 'heading')
            for label in ('credentials.access', 'credentials.secret', 'credentials.token'):
                blank_secret(en, label)
            # Save the existing account with empty secret fields. The same-scoped
            # keyring item must remain usable by the real backend after restart.
            press(en['connections.save'])
            press(en['connections.connectNamed'].format(name=name))
            lookup(en['nav.objects'], 'heading')
            lookup(bucket, 'button')
            bandwidth(en, save=True)
        with desktop(FIXTURE / 'S3S', root):
            lookup(en['connections.actionEdit'].format(name=name), 'button')
            bandwidth(en, save=False)
            visit(en, 'connections')
            press(en['connections.connectNamed'].format(name=name))
            lookup(en['nav.objects'], 'heading')
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
            check_regional_workspace(root,en,'tos','TOS Beijing','cn-beijing',
                                     'https://tos-cn-beijing.volces.com','design-assets',credentials)
            check_regional_workspace(root,en,'oss','OSS Hangzhou','cn-hangzhou',
                                     'https://oss-cn-hangzhou.aliyuncs.com','project-archive',credentials)
        check_storage(root,(credentials,))
    print('PASS real AWS/TOS/OSS account actions, scoped reads, diagnostics, native metadata and clipboard, '
          'selection across navigation, Linux keyring, storage scan, custom bandwidth after restart and native file transfers')


if __name__ == '__main__':
    verify()
