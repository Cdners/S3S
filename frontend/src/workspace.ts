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
export function call<T>(method: string, ...args: unknown[]): Promise<T> {
  return request<T>(method, args)
}
let profileRevision = 0
export async function loadProfiles() {
  const revision = profileRevision
  const saved = await call<Profile[]>('Connections')
  if (revision !== profileRevision) return
  const existing = new Map(profiles.value.map(item => [item.id,item]))
  // Preserve open bucket views and probe results across harmless navigation
  // refreshes. A changed profile (including a credential write) gets a new identity.
  profiles.value = saved.map(item => {
    const previous = existing.get(item.id)
    return previous && previous.provider === item.provider && previous.endpoint === item.endpoint &&
      previous.name === item.name && previous.region === item.region ? previous : item
  })
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
  profileRevision++
  profiles.value = [...profiles.value.filter(item => item.id !== saved.id), saved].sort((a,b) => a.name.localeCompare(b.name))
  return { profile: saved, credentialError: error }
}

export interface Capabilities {
  listBuckets:boolean; listObjects:boolean; metadata:boolean; download:boolean; upload:boolean; resumeParts:boolean
}
const capabilities = ref<Partial<Record<Provider,Capabilities>>>({})
export async function loadCapabilities() {
  capabilities.value = await call<Record<Provider,Capabilities>>('Capabilities')
}
export const capabilitiesFor = (provider: Provider) => capabilities.value[provider]
export interface ProbeResult {operation:'listBuckets' | 'listObjects'; latencyMs:number}
export const testConnection = (profile:Profile, bucket:string, signal:AbortSignal) =>
  request<ProbeResult>('TestConnection', [profile,bucket], signal)
export interface ObjectMetadata {size:number; etag:string; versionId?:string}
export const objectMetadata = (profile:Profile, bucket:string, key:string, signal:AbortSignal) =>
  request<ObjectMetadata>('ObjectMetadata', [profile,bucket,key], signal)
export async function copyObjectKey(key:string) {
  const { Clipboard } = await import('@wailsio/runtime')
  await Clipboard.SetText(key)
}
export interface SecretInput {accessKeyId: string; secretAccessKey: string; sessionToken: string}
export interface ObjectInfo {key: string; size: number; etag: string}
interface BucketPage {buckets: string[]; nextToken: string}
interface ObjectPage {objects: ObjectInfo[]; prefixes: string[]; nextToken: string}
export const listBuckets = (profile: Profile, token: string, signal: AbortSignal) =>
  request<BucketPage>('ListBuckets', [profile, token], signal)
export const listObjects = (profile: Profile, bucket: string, prefix: string, token: string, signal: AbortSignal) =>
  request<ObjectPage>('ListObjects', [profile, bucket, prefix, token], signal)
export const startDownload = (profile: Profile, bucket: string, key: string, title: string) =>
  call<string>('StartDownload', profile, bucket, key, title)
export const pauseDownload = (id: string) => call<boolean>('PauseDownload', id)
export const resumeDownload = (id: string) => call<boolean>('ResumeDownload', id)
export async function forgetCredentials(profile: Profile) {
  await call<boolean>('ForgetCredentials', profile)
  profileRevision++
  profiles.value = profiles.value.map(item => item.id === profile.id ? {...item} : item)
}

export const startUpload = (profile: Profile, bucket: string, key: string, title: string, signal: AbortSignal) =>
  request<string>('StartUpload', [profile, bucket, key, title], signal)
export const pauseUpload = (id: string) => call<boolean>('PauseUpload', id)
export const resumeUpload = (id: string) => call<boolean>('ResumeUpload', id)
export const cancelUpload = (id: string) => call<boolean>('CancelUpload', id)
