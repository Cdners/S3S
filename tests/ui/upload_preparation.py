"""Verify cancellation at the production Vue-to-Wails bridge, using an isolated HTTP fixture."""
from playwright.sync_api import expect


def verify_upload_preparation(page, address, english):
    # The fixture controls only the local Wails transport. Keep the original
    # StartUpload HTTP request pending so the real JS runtime has to cancel it.
    page.add_init_script("""
        window._wails = { environment: { OS: 'linux' } };
        localStorage.setItem('s3s.locale', 'en-US');
    """)
    profile = {'id': 'fixture', 'name': 'Test profile', 'provider': 'aws', 'endpoint': '', 'region': ''}
    pending = []
    canceled = []
    methods = []
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))

    def transport(route):
        request = route.request.post_data_json
        args = request['args']
        if request['object'] == 10:  # Wails CancelCall, independent of task-level CancelUpload.
            canceled.append(args['call-id'])
            route.fulfill(json=None, status=204)
            return
        method = args['methodName'].split('.')[-1]
        methods.append(method)
        if method == 'StartUpload':
            pending.append((route, args['call-id']))
            return
        results = {
            'Connections': [profile],
            'ListObjects': {'objects': [], 'prefixes': [], 'nextToken': ''},
            'Tasks': [],
        }
        route.fulfill(json={'data': results[method], 'error': None})

    page.route('**/wails/runtime', transport)
    page.goto(address)
    page.locator('[data-page="objects"]').click()
    page.locator('#cloud-profile').select_option(profile['id'])
    page.locator('#bucket-input').fill('test-bucket')
    page.get_by_role('button', name=english['objects.openBucket']).click()
    page.get_by_role('button', name=english['objects.upload']).click()
    page.locator('#upload-key').fill('original//destination')
    with page.expect_request(lambda req: req.url.endswith('/wails/runtime')
                             and req.post_data_json['args'].get('methodName', '').endswith('.StartUpload')):
        page.get_by_role('button', name=english['upload.chooseFile']).click()
    with page.expect_response(lambda res: res.url.endswith('/wails/runtime')
                              and res.request.post_data_json['object'] == 10):
        page.get_by_role('button', name=english['upload.stopPreparation']).click()
    assert len(pending) == 1 and canceled == [pending[0][1]]
    expect(page.get_by_role('dialog')).to_have_count(0)
    expect(page.get_by_role('status')).to_contain_text(english['upload.stopRequested'])

    # Cancellation can race a successful enqueue. A late reply must not turn
    # the notice into a success, close a new dialog or abort the durable task.
    page.get_by_role('button', name=english['objects.upload']).click()
    with page.expect_response(lambda res: res.url.endswith('/wails/runtime')
                              and res.request.post_data_json['args'].get('methodName', '').endswith('.StartUpload')):
        pending[0][0].fulfill(json={'data': 'already-queued-task', 'error': None})
    expect(page.get_by_role('dialog')).to_be_visible()
    page.get_by_role('button', name=english['action.cancel'], exact=True).click()
    expect(page.get_by_role('status')).to_contain_text(english['upload.stopRequested'])
    page.locator('[data-page="tasks"]').click()
    expect(page.get_by_role('heading', level=1)).to_have_text(english['nav.tasks'])
    assert 'CancelUpload' not in methods and not errors
