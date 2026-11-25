import type { Task } from './workspace'

export type TaskAction = 'pause' | 'resume' | 'cancel' | 'cleanup'

// All status and progress come from the durable queue. A poll may observe a
// newer task between opening an action dialog and confirming it.
export function taskActions(task: Pick<Task,'direction' | 'state' | 'resumable' | 'cancelRequested'>): TaskAction[] {
  if (!task.resumable || !['upload', 'download'].includes(task.direction)) return []
  const idle = ['queued','paused','failed'].includes(task.state)
  if (!idle && task.state !== 'running') return []
  if (task.direction === 'upload' && task.cancelRequested) return idle ? ['cleanup'] : []
  const action = task.state === 'running' ? 'pause' : 'resume'
  return task.direction === 'upload' ? [action, 'cancel'] : [action]
}
