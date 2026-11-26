"""Check rate units and acknowledgements at the production Vue/Wails boundary."""
from playwright.sync_api import expect


def verify_bandwidth(page, address, english, chinese):
    page.add_init_script("window._wails = {environment: {OS: 'linux'}}")
    saved = {'mode': 'office', 'uploadBytesPerSecond': 1048576, 'downloadBytesPerSecond': 4194304}
    writes = []
    failed_once = False

    def transport(route):
        nonlocal saved, failed_once
        args = route.request.post_data_json['args']
        method = args['methodName'].split('.')[-1]
        if method == 'SaveTransferOptions':
            writes.append(args['args'][0])
            if not failed_once:
                failed_once = True
                route.fulfill(json={'data': None, 'error': {'code': 'error.storage'}})
                return
            saved = writes[-1]
        replies = {'Connections': [], 'Capabilities': {}, 'GetTransferOptions': saved,
                   'SaveTransferOptions': saved, 'Tasks': []}
        route.fulfill(json={'data': replies[method], 'error': None})

    page.route('**/wails/runtime', transport)
    page.goto(address)
    page.locator('[data-page="settings"]').click()
    mode = page.locator('#speed-mode')
    expect(mode).to_have_value('office')
    mode.select_option('custom')
    page.locator('#upload-limit').fill('64.5')
    page.locator('#download-limit').fill('0')
    page.locator('aside').get_by_role('radio', name=english['language.nativeName']).click()
    expect(page.locator('#upload-limit')).to_have_value('64.5')
    apply = page.get_by_role('button', name=english['bandwidth.apply'])
    apply.click()
    expect(page.get_by_role('alert')).to_contain_text(english['error.storage'])
    expect(page.get_by_text(english['bandwidth.saved'])).to_have_count(0)
    apply.click()
    expect(page.get_by_text(english['bandwidth.saved'])).to_be_visible()
    assert writes == [{'mode': 'custom', 'uploadBytesPerSecond': 66048, 'downloadBytesPerSecond': 0}] * 2
    expect(apply).to_be_disabled()
    page.reload()
    page.locator('[data-page="settings"]').click()
    expect(mode).to_have_value('custom')
    expect(page.locator('#upload-limit')).to_have_value('64.5')
    page.locator('#download-limit').fill('-1')
    expect(apply).to_be_disabled()
    page.locator('aside').get_by_role('radio', name=chinese['language.nativeName']).click()
    mode.select_option('full')
    page.get_by_role('button', name=chinese['bandwidth.apply']).click()
    expect(page.get_by_text(chinese['bandwidth.saved'])).to_be_visible()
    assert saved == {'mode': 'full', 'uploadBytesPerSecond': 0, 'downloadBytesPerSecond': 0}
