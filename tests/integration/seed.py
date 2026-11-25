"""Populate only the loopback Moto service in its private network namespace."""
import hashlib
import json
import secrets
import sys
import time
from pathlib import Path

import boto3
from botocore.config import Config
from botocore.exceptions import EndpointConnectionError


fixture = Path('/fixture')
credentials = json.loads((fixture / 'credentials.json').read_text())
client = boto3.client('s3', region_name='us-east-1', endpoint_url='https://s3.us-east-1.amazonaws.com',
                      verify=str(fixture / 'server.crt'), aws_access_key_id=credentials['accessKeyId'],
                      aws_secret_access_key=credentials['secretAccessKey'],
                      config=Config(proxies={}, retries={'max_attempts': 0}, s3={'addressing_style': 'path'}))
for attempt in range(40):
    try:
        client.list_buckets()
        break
    except EndpointConnectionError:
        if attempt == 39:
            raise
        time.sleep(.25)
bucket = 's3s-fixture'
if len(sys.argv) > 1:
    # Independent inspection after Go tests, including jobs that exercised a
    # failed conditional completion followed by explicit cancellation.
    if client.list_multipart_uploads(Bucket=bucket).get('Uploads'):
        raise AssertionError('A multipart session survived the integration run')
    if sys.argv[1] == 'desktop':
        result = client.get_object(Bucket=bucket, Key='ui-upload +%.bin')
        downloaded = result['Body'].read()
        expected = hashlib.shake_256(b'S3S UI upload').digest((8 << 20) + 37)
        if downloaded != expected or result['Metadata'].get('s3s-sha256') != hashlib.sha256(expected).hexdigest():
            raise AssertionError('Native upload bytes or source digest differ from the selected file')
        print('PASS independent readback of native multipart upload, exact bytes and source metadata')
    print('PASS independent scan for unclosed multipart sessions')
    sys.exit(0)

client.create_bucket(Bucket=bucket)
# Force at least two ListObjectsV2 pages plus a distinct CommonPrefixes entry.
for index in range(103):
    client.put_object(Bucket=bucket, Key=f'page/{index:03}', Body=b'')
client.put_object(Bucket=bucket, Key='page/child/leaf', Body=b'')
key = '原样//+%2F segment/../bytes.bin'
body = secrets.token_bytes(18 * 1024 * 1024 + 319)
client.put_object(Bucket=bucket, Key=key, Body=body)
# A compact human-readable row for the native UI, exposed at the bucket root.
client.put_object(Bucket=bucket, Key='ui-proof.txt', Body=b'S3S native browser fixture\n')
print(json.dumps({'bucket': bucket, 'key': key, 'size': len(body), 'sha256': hashlib.sha256(body).hexdigest()}))
