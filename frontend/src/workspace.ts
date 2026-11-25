import { ref } from 'vue'
import type { MessageKey } from './i18n'
import en from './locales/en-US.json'

export const providers = ['aws', 'r2', 'oss', 'tos', 's3-compatible'] as const
export type Provider = typeof providers[number]
export interface Profile { id: string; name: string; provider: Provider; endpoint: string; region: string }
export interface Task { id: string; connectionId: string; direction: string; bucket: string; objectKey: string; state: string; size: number }
interface ErrorDetail {
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
export function workspaceError(err: unknown): WorkspaceError {
  return err instanceof WorkspaceError ? err : new WorkspaceError('error.bridge')
}
async function call<T>(method: string, ...args: unknown[]): Promise<T> {
  if (!isDesktop) throw new WorkspaceError('error.preview')
  const { Call } = await import('@wailsio/runtime')
  const result: Result<T> = await Call.ByName('main.Workspace.' + method, ...args)
  if (result.error) {
    const { code } = result.error
    const key = code.startsWith('error.') && Object.hasOwn(en, code) ? code as MessageKey : 'error.storage'
    throw new WorkspaceError(key, result.error)
  }
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
