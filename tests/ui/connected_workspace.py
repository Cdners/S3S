"""Exercise authenticated workspace transitions with independently controllable Wails replies."""
from playwright.sync_api import expect

from capabilities import CAPABILITIES


def verify_connected_workspace(page, address, catalogs):
    page.add_init_script("window._wails = {environment: {OS: 'linux'}}")
    accounts = [
        {'id': 'a', 'name': 'Engineering archive', 'provider': 'aws', 'region': 'us-east-1',
         'endpoint': 'https://s3.us-east-1.amazonaws.com'},
        {'id': 'b', 'name': 'Design team', 'provider': 'tos', 'region': 'cn-beijing',
         'endpoint': 'https://tos-cn-beijing.volces.com'},
    ]
    requested = []
    held = []
    held_probe = []
    bucket, key = 'design-assets', '项目//+%2F.txt'
    deny = {'code': 'error.cloudAccess', 'providerCode': 'AccessDenied', 'requestId': 'isolated-request'}

    def reply(route, data=None, error=None):
        route.fulfill(json={'data': data, 'error': error})

    def transport(route):
        request = route.request.post_data_json
        if request['object'] == 10:  # A superseded Wails request may still produce a late reply.
            route.fulfill(status=204)
            return
        rpc = request['args']
        method = rpc['methodName'].split('.')[-1]
        args = rpc['args']
        if method in ('Capabilities', 'Connections', 'Tasks'):
            reply(route, {'Capabilities': CAPABILITIES, 'Connections': accounts, 'Tasks': []}[method])
            return
        requested.append((method, args))
        if method == 'ListBuckets':
            if args[0]['id'] == 'a':
                held.append(route)
                page.evaluate('window.fixtureBucketRequests = (window.fixtureBucketRequests || 0) + 1')
                return
            reply(route, error=deny)
        elif method == 'TestConnection':
            if args[1] == 'previous-scope-test':
                held_probe.append(route)
                page.evaluate('window.fixtureProbeReceived = true')
                return
            if args[0]['id'] == 'b' and not args[1]:
                reply(route, error=deny)
            else:
                reply(route, {'operation': 'listObjects' if args[1] else 'listBuckets', 'latencyMs': 17})
        elif method == 'ListObjects':
            assert args[0]['id'] == 'b' and args[1] == bucket
            reply(route, {'objects': [{'key': key, 'size': 12, 'etag': '"listing"'}],
                  'prefixes': [], 'nextToken': ''})
        elif method == 'ObjectMetadata':
            assert args[0]['id'] == 'b' and args[1:] == [bucket, key]
            reply(route, {'size': 12, 'etag': '"from-head"', 'versionId': 'fixture-version'})
        else:
            raise AssertionError('Unexpected workspace RPC: '+method)

    page.route('**/wails/runtime', transport)
    page.goto(address)
    expect(page.locator('[data-connect]').first).to_be_enabled()
    assert not requested
    zh, en = catalogs
    for size in [(760, 560), (1024, 768), (1200, 800)]:
        page.set_viewport_size({'width': size[0], 'height': size[1]})
        for catalog in (zh, en):
            page.locator('aside').get_by_role('radio', name=catalog['language.nativeName']).check()
            rows = page.locator('.account-row')
            for row in rows.all():
                buttons = [row.locator(sel).bounding_box() for sel in (
                    '[data-connect]', '[data-test-account]', '[data-edit-account]')]
                panel = page.locator('.accounts-region .panel').bounding_box()
                edge = min(size[0], panel['x'] + panel['width'])
                for button in buttons:
                    assert 0 <= button['x'] <= edge - button['width'], 'Account action exceeds panel'
                centers = [r['y']+r['height']/2 for r in buttons]
                assert max(centers)-min(centers) < 2, 'Account actions no longer share a centerline'
                assert all(a['x']+a['width'] <= b['x']
                           for a, b in zip(buttons, buttons[1:])), 'Overlapping account actions'
            assert page.evaluate('document.documentElement.scrollWidth === innerWidth')
            stage = page.locator('.account-toolbar')
            boxes = [stage.locator(s).bounding_box() for s in ('input', 'select')]
            assert abs(boxes[0]['height']-boxes[1]['height']) <= 1, 'Mismatched filter heights'
    assert not requested
    status = page.locator('[data-probe-result]:visible')
    expect(status).to_have_count(0)
    page.locator('[data-test-account]').first.click()
    expect(status).to_contain_text('Engineering archive')
    expect(status).to_contain_text('17')
    page.locator('[data-test-account]').last.click()
    expect(status).to_contain_text('AccessDenied')
    expect(status).to_contain_text('Design team')
    expect(status).not_to_contain_text('Engineering archive')

    with page.expect_request(lambda req: req.url.endswith('/wails/runtime') and
                             req.post_data_json['args'].get('methodName', '').endswith('.ListBuckets')):
        page.locator('[data-connect]').first.click()
    selector = page.locator('#cloud-profile')
    expect(selector).to_have_value('a')
    page.wait_for_function('window.fixtureBucketRequests === 1')
    selector.select_option('b')
    expect(page.get_by_role('button', name=en['objects.loadBuckets'])).to_be_enabled()
    assert len(held) == 1
    with page.expect_response(lambda res: res.url.endswith('/wails/runtime') and
                              res.request.post_data_json['args'].get('methodName', '').endswith('.ListBuckets')):
        reply(held.pop(), {'buckets': ['previous-account-bucket'], 'nextToken': ''})
    expect(page.get_by_text('previous-account-bucket')).to_have_count(0)
    expect(status).to_have_count(0)
    expect(page.get_by_role('alert')).to_contain_text('isolated-request')
    assert [args[0]['id'] for method, args in requested if method == 'ListBuckets'] == ['a', 'b']
    page.locator('#bucket-input').fill(bucket)
    page.locator('[data-test-bucket]').click()
    expect(status).to_contain_text(bucket)
    expect(status).to_contain_text('17')
    page.get_by_role('button', name=en['objects.openBucket']).click()
    expect(page.get_by_text(key, exact=True)).to_be_visible()
    page.locator('[data-page="tasks"]').click()
    page.locator('[data-page="objects"]').click()
    expect(selector).to_have_value('b')
    expect(page.get_by_text(key, exact=True)).to_be_visible()
    expect(page.get_by_role('button', name=en['objects.upload'])).to_have_count(0)
    page.locator('[data-object-details]').click()
    dialog = page.get_by_role('dialog')
    expect(dialog).to_contain_text('"from-head"')
    expect(dialog).to_contain_text('fixture-version')
    page.keyboard.press('Escape')
    expect(dialog).to_have_count(0)
    page.locator('#bucket-input').fill('previous-scope-test')
    page.locator('[data-test-bucket]').click()
    expect(status).to_contain_text('previous-scope-test')
    page.wait_for_function('window.fixtureProbeReceived === true')
    selector.select_option('a')
    expect(status).to_have_count(0)
    assert len(held_probe) == 1
    with page.expect_response(lambda res: res.url.endswith('/wails/runtime') and
                              res.request.post_data_json['args'].get('methodName', '').endswith('.TestConnection')):
        reply(held_probe.pop(), {'operation': 'listObjects', 'latencyMs': 42})
    page.wait_for_function('window.fixtureBucketRequests === 2')
    assert len(held) == 1
    reply(held.pop(), {'buckets': [], 'nextToken': ''})
    expect(page.get_by_text('previous-scope-test')).to_have_count(0)
    expect(page.get_by_text(key, exact=True)).to_have_count(0)
    expect(page.get_by_role('alert')).to_have_count(0)
    expect(status).to_have_count(0)
