"""Drive a built Linux WebView with AT-SPI and X11 in an isolated test profile."""
import argparse
import json
import os
import signal
from pathlib import Path
import subprocess
import tempfile
import time
from contextlib import contextmanager, suppress

import pyatspi


def descendants(node):
    yield node
    for child in node:
        if child:
            yield from descendants(child)


def lookup(name, role=None):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        try:
            for node in descendants(pyatspi.Registry.getDesktop(0)):
                if role and node.getRoleName() != role:
                    continue
                if node.name == name:
                    return node
                if not node.name and 'Text' in node.get_interfaces():
                    if node.queryText().getText(0, -1) == name:
                        return node
        except (RuntimeError, LookupError):
            # WebKit can replace accessibility objects during navigation.
            pass
        time.sleep(0.1)
    raise AssertionError(f'Missing {role or "accessible object"}: {name}')


def press(label, role='button'):
    action = lookup(label, role).queryAction()
    if not action.nActions or not action.doAction(0):
        raise AssertionError(f'Button was not activated: {label}')


def visit(catalog, route):
    title = catalog[f'nav.{route}']
    press(title)
    lookup(title, 'heading')


@contextmanager
def desktop(binary, profile):
    env = os.environ.copy()
    for variable in ('XDG_CONFIG_HOME', 'XDG_DATA_HOME', 'XDG_CACHE_HOME', 'XDG_RUNTIME_DIR'):
        path = profile / variable
        path.mkdir(mode=0o700, exist_ok=True)
        env[variable] = str(path)
    env.update(GDK_BACKEND='x11', GTK_A11Y='atspi', GSK_RENDERER='cairo',
               LIBGL_ALWAYS_SOFTWARE='1', MESA_SHADER_CACHE_DISABLE='true', GSETTINGS_BACKEND='memory')
    # Keep WebKit helpers alive while Wails handles SIGTERM as application Quit.
    # Stopping the renderer group first can discard pending localStorage writes.
    app = subprocess.Popen([binary], env=env, start_new_session=True)
    try:
        yield
    finally:
        app.terminate()
        with suppress(subprocess.TimeoutExpired):
            app.wait(timeout=6)
        with suppress(ProcessLookupError):
            os.killpg(app.pid, signal.SIGKILL)
        app.wait()
        deadline = time.monotonic() + 5
        while True:
            try:
                os.killpg(app.pid, 0)
            except ProcessLookupError:
                break
            if time.monotonic() > deadline:
                raise AssertionError('WebView process group survived teardown')
            time.sleep(0.05)


def check_action_layout(name, locales):
    # GTK/WebKit and Chromium use different native font metrics. Read the
    # actual rendered button rectangles after resizing the real X11 window.
    window = subprocess.check_output(['xdotool', 'search', '--onlyvisible', '--name', '^S3S$']).splitlines()[-1]
    for width, height in [(760, 560), (1024, 768), (1200, 800)]:
        subprocess.run(['xdotool', 'windowmove', window, '0', '0', 'windowsize', '--sync', window,
                        str(width), str(height)], check=True, timeout=8)
        for catalog in locales:
            press(catalog['language.nativeName'], 'radio button')
            lookup(catalog['nav.connections'], 'heading')
            buttons = [lookup(catalog[key].format(name=name), 'button') for key in
                       ('connections.connectNamed', 'connections.testNamed', 'connections.actionEdit')]
            deadline = time.monotonic() + 3
            while True:
                boxes = [button.queryComponent().getExtents(pyatspi.DESKTOP_COORDS) for button in buttons]
                visible = all(w > 0 and h > 0 and 0 <= x <= width-w and 0 <= y <= height-h for x,y,w,h in boxes)
                centers = [y + h/2 for _,y,_,h in boxes]
                separate = all(a[0] + a[2] <= b[0] for a,b in zip(boxes, boxes[1:]))
                if visible and separate and max(centers) - min(centers) <= 1:
                    break
                if time.monotonic() >= deadline:
                    raise AssertionError(f'Misaligned account actions at {width}×{height}: {boxes}')
                time.sleep(.05)


def verify(binary, locales, profile):
    zh, en = [json.loads((locales / f'{code}.json').read_text(encoding='utf-8'))
              for code in ('zh-CN', 'en-US')]
    name = 'S3S UI smoke'
    with desktop(binary, profile):
        lookup(zh['connections.empty'], 'heading')
        for route in ('objects', 'tasks', 'settings'):
            visit(zh, route)
        press(en['language.nativeName'], 'radio button')
        lookup(en['nav.settings'], 'heading')
        visit(en, 'connections')
        press(en['connections.add'])
        lookup(en['connections.add'], 'heading')
        subprocess.run(['xdotool', 'type', '--clearmodifiers', name], check=True)
        press(en['connections.save'])
        lookup(en['connections.actionEdit'].format(name=name), 'button')
        check_action_layout(name, [zh, en])
        visit(en, 'objects')
        lookup(en['objects.empty'], 'heading')
        visit(en, 'tasks')
        lookup(en['tasks.empty'], 'heading')
    with desktop(binary, profile):
        lookup(en['nav.connections'], 'heading')
        lookup(en['connections.actionEdit'].format(name=name), 'button')
    print('PASS Linux desktop navigation, six bilingual layout checks, profile save and persistence after restart')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=Path)
    parser.add_argument('locales', type=Path)
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix='s3s-ui-') as folder:
        verify(args.binary.resolve(), args.locales, Path(folder))
