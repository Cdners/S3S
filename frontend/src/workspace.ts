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
async function call<T>(method: string, ...args: unknown[]): Promise<T> {
  if (!isDesktop) throw new WorkspaceError('error.preview')
  const { Call } = await import('@wailsio/runtime')
  const result: Result<T> = await Call.ByName('main.Workspace.' + method, ...args)
  if (result.error) throw localizedError(result.error)
  if (result.data === null) throw new WorkspaceError('error.storage')
  return result.data
}
export async function loadProfiles() {
  profiles.value = await call<Profile[]>('Connections')
}
export async function loadTasks() {
  tasks.value = await call<Task[]>('Tasks')
}
export async function saveProfile(input: Omit<Profile, 'id'> & { id?: string }) {
  const saved = await call<Profile>('SaveConnection', input)
  profiles.value = [...profiles.value.filter(item => item.id !== saved.id), saved].sort((a,b) => a.name.localeCompare(b.name))
}

export const supportsCloud = (profile: Profile) => profile.provider === 'aws' || profile.provider === 'r2'
export interface SecretInput {accessKeyId: string; secretAccessKey: string; sessionToken: string}
export interface ObjectInfo {key: string; size: number; etag: string}
interface BucketPage {buckets: string[]; nextToken: string}
interface ObjectPage {objects: ObjectInfo[]; prefixes: string[]; nextToken: string}
export const setCredentials = (profile: Profile, credentials: SecretInput) =>
  call<boolean>('SetCredentials', profile, credentials)
export const listBuckets = (profile: Profile, token: string) =>
  call<BucketPage>('ListBuckets', profile, token)
export const listObjects = (profile: Profile, bucket: string, prefix: string, token: string) =>
  call<ObjectPage>('ListObjects', profile, bucket, prefix, token)
export const startDownload = (profile: Profile, bucket: string, key: string, title: string) =>
  call<string>('StartDownload', profile, bucket, key, title)
export const pauseDownload = (id: string) => call<boolean>('PauseDownload', id)
export const resumeDownload = (id: string) => call<boolean>('ResumeDownload', id)
export const forgetCredentials = (profile: Profile) => call<boolean>('ForgetCredentials', profile)

export const startUpload = (profile: Profile, bucket: string, key: string, title: string) =>
  call<string>('StartUpload', profile, bucket, key, title)
export const pauseUpload = (id: string) => call<boolean>('PauseUpload', id)
export const resumeUpload = (id: string) => call<boolean>('ResumeUpload', id)
export const cancelUpload = (id: string) => call<boolean>('CancelUpload', id)
