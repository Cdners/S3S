import type { Provider } from './workspace'

// Display-only examples. Account creation always starts with empty values.
export const providerGuides: Record<Provider, {region: string; endpoint: string}> = {
  aws: { region: 'ap-southeast-1', endpoint: 'https://s3.ap-southeast-1.amazonaws.com' },
  r2: { region: 'auto', endpoint: 'https://<ACCOUNT_ID>.r2.cloudflarestorage.com' },
  oss: { region: 'cn-hangzhou', endpoint: 'https://oss-cn-hangzhou.aliyuncs.com' },
  tos: { region: 'cn-beijing', endpoint: 'https://tos-cn-beijing.volces.com' },
  's3-compatible': { region: 'us-east-1', endpoint: 'https://s3.example.com' }
}
