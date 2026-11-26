import { onBeforeUnmount, shallowRef } from 'vue'
import { testConnection, workspaceError, type Profile, type ProbeResult, type WorkspaceError } from './workspace'

interface Scope { profile:Profile; bucket:string }
export type Probe = Scope & (
  {status:'pending'} |
  {status:'success'; result:ProbeResult} |
  {status:'failure'; error:WorkspaceError}
)

export function useConnectionProbe() {
  const probe = shallowRef<Probe>()
  let controller: AbortController | undefined
  function clear() {
    controller?.abort()
    controller = undefined
    probe.value = undefined
  }
  async function run(profile:Profile, bucket = '') {
    clear()
    const active = new AbortController()
    controller = active
    probe.value = {profile,bucket,status:'pending'}
    try {
      const result = await testConnection(profile,bucket,active.signal)
      if (controller === active) probe.value = {profile,bucket,status:'success',result}
    } catch (error) {
      if (controller === active) probe.value = {profile,bucket,status:'failure',error:workspaceError(error)}
    } finally {
      if (controller === active) controller = undefined
    }
  }
  onBeforeUnmount(clear)
  return {probe,run,clear}
}
