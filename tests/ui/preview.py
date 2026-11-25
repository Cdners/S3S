"""Exercise the built, standalone preview using Playwright and system Chromium."""
import json
import shutil
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread

from playwright.sync_api import expect, sync_playwright

from upload_preparation import verify_upload_preparation


FRONTEND = Path(__file__).resolve().parents[2] / 'frontend'


def messages(locale):
    path = FRONTEND / 'src' / 'locales' / f'{locale}.json'
    return json.loads(path.read_text(encoding='utf-8'))


def open_page(page, catalog, route):
    button = page.locator(f'[data-page="{route}"]')
    button.click()
    expect(button).to_have_attribute('aria-current', 'page')
    expect(page.get_by_role('heading', level=1)).to_have_text(catalog[f'nav.{route}'])


def import_pack(page, translations):
    pack = {'locale': 'fr', 'name': 'Français', 'messages': translations}
    page.locator('#language-file').set_input_files({
        'name': 'fr.json', 'mimeType': 'application/json', 'buffer': json.dumps(pack).encode(),
    })


def verify(page, address):
    english, chinese = messages('en-US'), messages('zh-CN')
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.goto(address)
    expect(page.locator('html')).to_have_attribute('lang', 'zh-CN')
    for route in ('connections', 'objects', 'tasks', 'settings'):
        open_page(page, chinese, route)
    page.locator('#language').select_option('en-US')
    expect(page.locator('html')).to_have_attribute('lang', 'en-US')
    open_page(page, english, 'connections')
    expect(page.get_by_role('button', name=english['connections.add'])).to_be_disabled()
    expect(page.get_by_text(english['error.preview'])).to_be_visible()
    open_page(page, english, 'objects')
    expect(page.get_by_text(english['objects.empty'], exact=True)).to_be_visible()
    open_page(page, english, 'tasks')
    expect(page.get_by_text(english['tasks.emptyText'], exact=True)).to_be_visible()
    page.reload()
    expect(page.locator('html')).to_have_attribute('lang', 'en-US')
    open_page(page, english, 'settings')

    import_pack(page, {'connections.count': 'profils'})
    expect(page.get_by_role('status')).to_have_text(english['settings.importError'])
    expect(page.locator('#language')).to_have_value('en-US')
    import_pack(page, {'nav.settings': 'Réglages'})
    expect(page.get_by_role('heading', level=1)).to_have_text('Réglages')
    expect(page.get_by_text(english['settings.appearance'], exact=True)).to_be_visible()
    page.reload()
    expect(page.locator('html')).to_have_attribute('lang', 'fr')
    expect(page.get_by_role('heading', level=1)).to_have_text(english['nav.connections'])
    page.set_viewport_size({'width': 760, 'height': 560})
    if not page.evaluate('document.documentElement.scrollWidth === window.innerWidth'):
        raise AssertionError('Horizontal overflow at the minimum window width')
    if errors:
        raise AssertionError(errors)


def main():
    executable = shutil.which('chromium') or shutil.which('google-chrome')
    if not executable:
        raise RuntimeError('The preview check requires Chromium or Google Chrome')
    handler = partial(SimpleHTTPRequestHandler, directory=str(FRONTEND / 'dist'))
    with ThreadingHTTPServer(('127.0.0.1', 0), handler) as server, sync_playwright() as playwright:
        Thread(target=server.serve_forever, daemon=True).start()
        try:
            browser = playwright.chromium.launch(executable_path=executable)
            try:
                address = f'http://127.0.0.1:{server.server_port}'
                verify(browser.new_page(viewport={'width': 1200, 'height': 800}), address)
                verify_upload_preparation(browser.new_page(), address, messages('en-US'))
            finally:
                browser.close()
        finally:
            server.shutdown()
    print('PASS preview navigation, language import, fallback, persistence and upload preparation cancellation')


if __name__ == '__main__':
    main()
