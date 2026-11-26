"""Offline read-only OSS/TOS fixture. Runs on loopback in the Moto network namespace."""
import hashlib
import hmac
import json
import ssl
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qsl, quote, unquote, urlsplit
from xml.etree import ElementTree as ET


PROVIDERS = {
    'tos-cn-beijing.volces.com': ('tos', 'cn-beijing', 'design-assets'),
    'oss-cn-hangzhou.aliyuncs.com': ('oss', 'cn-hangzhou', 'project-archive'),
}
BODY = b'Object metadata from the isolated provider fixture.\n'
ETAG = '"' + hashlib.md5(BODY, usedforsecurity=False).hexdigest() + '"'
KEY = '项目 +%2F.txt'
SECRETS = json.loads(Path('/fixture/credentials.json').read_text())


def verified(handler, provider, region, bucket):
    """Verify the SigV4 header for the fixture credential, including the requested path."""
    algorithm = provider.upper() + '4-HMAC-SHA256'
    auth = handler.headers.get('Authorization', '')
    if not auth.startswith(algorithm + ' '):
        return False
    fields = dict(item.strip().split('=', 1) for item in auth[len(algorithm)+1:].split(','))
    access_key, _, scope = fields['Credential'].partition('/')
    stamp = handler.headers[f'x-{provider}-date']
    suffix = 'request' if provider == 'tos' else 'aliyun_v4_request'
    if access_key != SECRETS['accessKeyId'] or scope != f'{stamp[:8]}/{region}/{provider}/{suffix}':
        return False
    url = urlsplit(handler.path)
    query = sorted((quote(k, safe=''), quote(v, safe=''))
                   for k, v in parse_qsl(url.query, keep_blank_values=True))
    if provider == 'tos':
        signed = fields['SignedHeaders']
        headers = signed.split(';')
        if not {'host', 'x-tos-date'}.issubset(headers):
            return False
        uri = quote(unquote(url.path), safe='/')
        search = '&'.join(k + '=' + v for k, v in query)
        default_hash = hashlib.sha256(b'').hexdigest()
        secret_prefix = ''
    else:
        signed = fields.get('AdditionalHeaders', '')
        headers = sorted({k.lower() for k in handler.headers if k.lower().startswith('x-oss-') or
                          k.lower() in ('content-type', 'content-md5')} | set(filter(None, signed.split(';'))))
        uri = ('/' + bucket if bucket else '') + quote(unquote(url.path), safe='/')
        search = '&'.join(k + ('=' + v if v else '') for k, v in query)
        default_hash = 'UNSIGNED-PAYLOAD'
        secret_prefix = 'aliyun_v4'
    canonical_headers = ''.join(f'{k}:{handler.headers[k].strip()}\n' for k in headers)
    payload_hash = handler.headers.get(f'x-{provider}-content-sha256', default_hash)
    canonical = '\n'.join([handler.command, uri, search, canonical_headers, signed, payload_hash])
    description = '\n'.join([algorithm, stamp, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    key = (secret_prefix + SECRETS['secretAccessKey']).encode()
    for part in scope.split('/'):
        key = hmac.digest(key, part.encode(), 'sha256')
    return hmac.compare_digest(hmac.new(key, description.encode(), 'sha256').hexdigest(), fields['Signature'])


class Fixture(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Never record request headers, query strings or credentials.

    def reply(self, code, data=b'', **headers):
        self.send_response(code)
        for key, value in headers.items():
            self.send_header(key, value)
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(data)

    def read(self):
        host = self.headers.get('Host', '')
        for domain, (provider, region, name) in PROVIDERS.items():
            if host == domain or host == name + '.' + domain:
                break
        else:
            self.reply(404)
            return
        bucket = name if host != domain else ''
        rid = f'isolated-{provider}-request'
        hdr = {f'x-{provider}-request-id': rid}
        try:
            authenticated = verified(self, provider, region, bucket)
        except (KeyError, ValueError, TypeError, AttributeError):
            authenticated = False
        # TOS denies account-wide listing but permits browsing a known bucket.
        if not authenticated or (provider == 'tos' and not bucket):
            code = 'AccessDenied' if authenticated else 'SignatureDoesNotMatch'
            if provider == 'tos':
                self.reply(403, json.dumps({'Code': code, 'Message': code, 'RequestId': rid}).encode(), **hdr)
            else:
                self.reply(
                    403, f'<Error><Code>{code}</Code><Message>{code}</Message><RequestId>{rid}</RequestId></Error>'.encode(), **hdr)
            return
        if not bucket:
            root = ET.Element('ListAllMyBucketsResult')
            ET.SubElement(ET.SubElement(ET.SubElement(root, 'Buckets'), 'Bucket'), 'Name').text = name
            self.reply(200, ET.tostring(root), **hdr)
            return
        url = urlsplit(self.path)
        if dict(parse_qsl(url.query)).get('list-type') == '2' and url.path == '/':
            if provider == 'tos':
                result = {'Contents': [{'Key': KEY, 'ETag': ETAG, 'Size': len(BODY)}], 'IsTruncated': False}
                self.reply(200, json.dumps(result).encode(), **hdr)
            else:
                root = ET.Element('ListBucketResult')
                ET.SubElement(root, 'EncodingType').text = 'url'
                ET.SubElement(root, 'IsTruncated').text = 'false'
                item = ET.SubElement(root, 'Contents')
                for tag, value in [('Key', quote(KEY, safe='')), ('ETag', ETAG), ('Size', str(len(BODY)))]:
                    ET.SubElement(item, tag).text = value
                self.reply(200, ET.tostring(root), **hdr)
            return
        if unquote(url.path) == '/' + KEY:
            if self.command == 'HEAD':
                self.reply(200, BODY, ETag=ETAG, **hdr)
                return
            if self.headers.get('If-Match') == ETAG and self.headers.get('Range') == 'bytes=7-14':
                self.reply(206, BODY[7:15], ETag=ETAG, **{'Content-Range': f'bytes 7-14/{len(BODY)}', **hdr})
                return
        self.reply(404, **hdr)

    do_GET = read
    do_HEAD = read


if __name__ == '__main__':
    server = ThreadingHTTPServer(('127.0.0.2', 443), Fixture)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain('/fixture/server.crt', '/cert/server.key')
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
