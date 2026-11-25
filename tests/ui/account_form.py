"""Exercise the production account form with a one-time keyring failure in its Wails transport."""
from secrets import token_hex

from playwright.sync_api import expect


def verify_account_form(page, address, catalog):
    page.add_init_script("window._wails = { environment: { OS: 'linux' } }")
    keys = {name: token_hex(12) for name in ('accessKeyId', 'secretAccessKey', 'sessionToken')}
    committed = None
    attempts = 0

    def transport(route):
        nonlocal committed, attempts
        args = route.request.post_data_json['args']
        method = args['methodName'].split('.')[-1]
        if method == 'Connections':
            route.fulfill(json={'data': [committed] if committed else [], 'error': None})
            return
        assert method == 'SaveAccount'
        previous, public, credentials = args['args']
        assert previous == committed
        assert public['id'] == (committed['id'] if committed else '')
        assert public['provider'] == 'tos'
        assert public['region'] == 'cn-beijing'
        assert public['endpoint'] == 'https://tos-cn-beijing.volces.com'
        if credentials != (keys if attempts < 2 else None):
            raise AssertionError('Unexpected credential replacement')
        attempts += 1
        committed = {**public, 'id': 'test-account'}
        # Public metadata committed even though the keyring rejected this first write.
        error = {'code': 'error.keyringUnavailable'} if attempts == 1 else None
        route.fulfill(json={'data': committed, 'error': error})

    def button(key):
        return page.get_by_role('button', name=catalog[key], exact=True)

    def enter_keys():
        for field, key in [('access', 'accessKeyId'), ('secret', 'secretAccessKey'), ('token', 'sessionToken')]:
            page.locator(f'#credential-{field}').fill(keys[key])

    page.route('**/wails/runtime', transport)
    page.goto(address)
    button('connections.add').click()
    name = 'Account form regression'
    page.locator('#profile-name').fill(name)
    enter_keys()
    expect(page.locator('[data-fill-example]')).to_have_count(0)
    page.locator('#profile-provider').select_option('tos')
    for field in ('access', 'secret', 'token'):
        assert not page.locator(f'#credential-{field}').input_value()
    page.locator('[data-fill-example]').click()
    expect(page.locator('#profile-region')).to_have_value('cn-beijing')
    expect(page.locator('#profile-endpoint')).to_have_value('https://tos-cn-beijing.volces.com')
    assert attempts == 0

    enter_keys()
    button('connections.save').click()
    expect(page.get_by_text(catalog['account.partialSave'])).to_be_visible()
    for field in ('access', 'secret', 'token'):
        assert not page.locator(f'#credential-{field}').input_value()
    enter_keys()
    button('connections.save').click()
    expect(page.get_by_role('dialog')).to_have_count(0)
    page.get_by_role('button', name=catalog['connections.actionEdit'].format(name=name)).click()
    assert not page.locator('#credential-secret').input_value()
    button('connections.save').click()
    expect(page.get_by_role('dialog')).to_have_count(0)
    assert attempts == 3
    storage = page.evaluate('JSON.stringify([localStorage, sessionStorage])')
    assert all(value not in storage for value in keys.values())
