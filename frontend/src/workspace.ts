import { ref } from 'vue'
import type { MessageKey } from './i18n'
import en from './locales/en-US.json'

export const providers = ['aws', 'r2', 'oss', 'tos', 's3-compatible'] as const
export type Provider = typeof providers[number]
export interface Profile { id: string; name: string; provider: Provider; endpoint: string; region: string }
export interface Task { id: string; connectionId: string; direction: string; bucket: string; objectKey: string; state: string; size: number; resumable: boolean; uploadPhase?: string; cancelRequested?: boolean; transferredBytes?: number; lastError?: ErrorDetail }
export interface ErrorDetail {
  code: string
  params?: Record<string, string>
  providerCode?: string
  requestId?: string
  retryable?: boolean
}
interface Result<T> { data: T | null; error: ErrorDetail | null }

const host = window as Window & {
  chrome?: { webview?: { postMessage?: unknown } }
  webkit?: { messageHandlers?: { external?: { postMessage?: unknown } } }
  _wails?: { environment?: unknown }
}
export const isDesktop = Boolean(host.chrome?.webview?.postMessage || host.webkit?.messageHandlers?.external?.postMessage || host._wails?.environment)
export const profiles = ref<Profile[]>([])
export const tasks = ref<Task[]>([])
export class WorkspaceError extends Error {
  constructor(readonly key: MessageKey, readonly detail?: ErrorDetail) { super(key) }
}
export function localizedError(detail: ErrorDetail): WorkspaceError {
  const { code } = detail
  const key = code.startsWith('error.') && Object.hasOwn(en, code) ? code as MessageKey : 'error.cloudRequest'
  return new WorkspaceError(key, detail)
}
export function workspaceError(err: unknown): WorkspaceError {
  return err instanceof WorkspaceError ? err : new WorkspaceError('error.bridge')
}
async function invoke<T>(method: string, args: unknown[], signal?: AbortSignal): Promise<Result<T>> {
  if (!isDesktop) throw new WorkspaceError('error.preview')
  const { Call } = await import('@wailsio/runtime')
  // The runtime's CancellablePromise sends a CancelCall to Go. Bind it before
  // awaiting, while the Wails call ID and cancellation callback are available.
  if (signal?.aborted) throw signal.reason
  const pending = Call.ByName('main.Workspace.' + method, ...args)
  return signal ? pending.cancelOn(signal) : pending
}
async function request<T>(method: string, args: unknown[], signal?: AbortSignal): Promise<T> {
  const result = await invoke<T>(method,args,signal)
  if (result.error) throw localizedError(result.error)
  if (result.data === null) throw new WorkspaceError('error.storage')
  return result.data
}
function call<T>(method: string, ...args: unknown[]): Promise<T> {
  return request<T>(method, args)
}
export async function loadProfiles() {
  profiles.value = await call<Profile[]>('Connections')
}
export async function loadTasks() {
  tasks.value = await call<Task[]>('Tasks')
}
// SaveAccount may persist public metadata before the OS keyring rejects a
// credential write. Keep the returned ID so a retry edits the same record.
export async function saveAccount(previous: Profile | null, changes: Profile, credentials: SecretInput | null) {
  const result = await invoke<Profile>('SaveAccount', [previous, changes, credentials])
  const error = result.error ? localizedError(result.error) : undefined
  if (!result.data) throw error ?? new WorkspaceError('error.storage')
  const saved = result.data
  profiles.value = [...profiles.value.filter(item => item.id !== saved.id), saved].sort((a,b) => a.name.localeCompare(b.name))
  return { profile: saved, credentialError: error }
}

export const supportsCloud = (profile: Pick<Profile, 'provider'>) => profile.provider === 'aws' || profile.provider === 'r2'
export interface SecretInput {accessKeyId: string; secretAccessKey: string; sessionToken: string}
export interface ObjectInfo {key: string; size: number; etag: string}
interface BucketPage {buckets: string[]; nextToken: string}
interface ObjectPage {objects: ObjectInfo[]; prefixes: string[]; nextToken: string}
export const listBuckets = (profile: Profile, token: string) =>
  call<BucketPage>('ListBuckets', profile, token)
export const listObjects = (profile: Profile, bucket: string, prefix: string, token: string) =>
  call<ObjectPage>('ListObjects', profile, bucket, prefix, token)
export const startDownload = (profile: Profile, bucket: string, key: string, title: string) =>
  call<string>('StartDownload', profile, bucket, key, title)
export const pauseDownload = (id: string) => call<boolean>('PauseDownload', id)
export const resumeDownload = (id: string) => call<boolean>('ResumeDownload', id)
export const forgetCredentials = (profile: Profile) => call<boolean>('ForgetCredentials', profile)

export const startUpload = (profile: Profile, bucket: string, key: string, title: string, signal: AbortSignal) =>
  request<string>('StartUpload', [profile, bucket, key, title], signal)
export const pauseUpload = (id: string) => call<boolean>('PauseUpload', id)
export const resumeUpload = (id: string) => call<boolean>('ResumeUpload', id)
export const cancelUpload = (id: string) => call<boolean>('CancelUpload', id)
