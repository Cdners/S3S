"""Backend capability fixture for isolated Wails transport tests."""
READ = {'listBuckets': True, 'listObjects': True, 'metadata': True, 'download': True, 'resumeParts': True}
CAPABILITIES = {provider: {**READ, 'upload': provider in ('aws', 'r2')}
                for provider in ('aws', 'r2', 'tos', 'oss', 's3-compatible')}
