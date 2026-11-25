"""Exercise the built, standalone preview using Playwright and system Chromium."""
import json
import shutil
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Thread

from playwright.sync_api import expect, sync_playwright

from account_form import verify_account_form
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
    # Change the language in-place while reading a specific provider's guide.
    open_page(page, chinese, 'connections')
    guide = page.locator('.account-examples')
    guide.locator('summary').click()
    guide.get_by_role('combobox').select_option('tos')
    expect(guide.get_by_text(chinese['account.guide.tos.keys'], exact=True)).to_be_visible()
    page.locator('aside').get_by_role('radio', name=chinese['language.nativeName']).press('ArrowRight')
    expect(page.get_by_role('heading', level=1)).to_have_text(english['nav.connections'])
    expect(page.locator('html')).to_have_attribute('lang', 'en-US')
    expect(guide.get_by_role('combobox')).to_have_value('tos')
    expect(guide.get_by_text('cn-beijing', exact=True)).to_be_visible()
    expect(guide.get_by_text('https://tos-cn-beijing.volces.com', exact=True)).to_be_visible()
    for provider in ('aws', 'r2', 'oss', 'tos', 's3-compatible'):
        guide.get_by_role('combobox').select_option(provider)
        for field in ('origin', 'keys', 'next'):
            expect(guide.get_by_text(english[f'account.guide.{provider}.{field}'], exact=True)).to_be_visible()
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

    expect(page.get_by_role('button', name=english['settings.import'])).to_be_hidden()
    page.locator('.language-packs summary').click()
    import_pack(page, {'connections.count': 'profils'})
    expect(page.get_by_role('status')).to_have_text(english['settings.importError'])
    expect(page.locator('main').get_by_role('radio', name=english['language.nativeName'])).to_be_checked()
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
                verify_account_form(browser.new_page(viewport={'width': 760, 'height': 560}), address, messages('zh-CN'))
                verify_upload_preparation(browser.new_page(), address, messages('en-US'))
            finally:
                browser.close()
        finally:
            server.shutdown()
    print('PASS five account guides, keyboard language switch with context, language import, fallback, '
          'persistence, account keyring retry, preview navigation and upload preparation cancellation')


if __name__ == '__main__':
    main()
