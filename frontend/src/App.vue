<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import {
  Activity, ArrowDownUp,
  FolderClosed, HardDrive, Info, Layers3, Pencil,
  Plus, Search, Server, Settings2, SlidersHorizontal, Waypoints
} from '@lucide/vue'
import ProfileDialog from './ProfileDialog.vue'
import AccountExamples from './AccountExamples.vue'
import ObjectBrowser from './ObjectBrowser.vue'
import ErrorNotice from './ErrorNotice.vue'
import CancelUploadDialog from './CancelUploadDialog.vue'
import { taskActions, type TaskAction } from './transferActions'
import LanguageSwitch from './LanguageSwitch.vue'
import LanguageSettings from './LanguageSettings.vue'
import { locale, t, type MessageKey } from './i18n'
import { workspaceError, isDesktop, loadProfiles, loadTasks, profiles, providers, tasks, supportsCloud, pauseDownload, resumeDownload, pauseUpload, resumeUpload, cancelUpload, localizedError, type Task, type Profile, type Provider, type WorkspaceError } from './workspace'

type Page = 'connections' | 'objects' | 'tasks' | 'settings'
const nav = [{ id:'connections', icon:Waypoints }, { id:'objects', icon:FolderClosed }, { id:'tasks', icon:ArrowDownUp }] as const
const page = ref<Page>('connections')
const query = ref('')
const filter = ref<Provider | ''>('')
const initialProvider = ref<Provider>('aws')
const pendingTask = ref('')
const cancelID = ref('')
const cancelTarget = computed(() => tasks.value.find(task => task.id === cancelID.value && taskActions(task).includes('cancel')))
const fetchedTasks = ref(false)
const taskFailure = ref<WorkspaceError>()
const editing = ref<Profile>()
const showDialog = ref(false)
const failures = ref<Partial<Record<'connections' | 'tasks', WorkspaceError>>>({})
const loading = ref({ connections:false, tasks:false })
const visible = computed(() => profiles.value.filter(p =>
  (!filter.value || filter.value === p.provider) && p.name.toLowerCase().includes(query.value.trim().toLowerCase())))
const dataPage = computed(() => page.value === 'tasks' ? 'tasks' : 'connections')
const failure = computed(() => page.value === 'settings' ? undefined : failures.value[dataPage.value])
const states: Record<string, MessageKey> = {
  queued:'state.queued', running:'state.running', paused:'state.paused', completed:'state.completed',
  failed:'state.failed', canceled:'state.canceled'
}
async function refresh(kind: 'connections' | 'tasks') {
  if (loading.value[kind]) return
  failures.value[kind] = undefined
  loading.value[kind] = true
  try {
    if (kind === 'connections') await loadProfiles()
    else { await loadTasks(); fetchedTasks.value = true }
  }
  catch (error) { failures.value[kind] = workspaceError(error) }
  finally { loading.value[kind] = false }
}
const uploadPhases: Record<string, MessageKey> = {
  ready:'upload.phaseReady', creating:'upload.phaseCreating', active:'upload.phaseActive',
  completing:'upload.phaseCompleting', aborting:'upload.phaseAborting'
}
function phaseLabel(task:Task): MessageKey | undefined {
  if (task.direction !== 'upload' || task.state === 'completed' || task.state === 'canceled') return
  if (task.cancelRequested) return 'upload.phaseCleanup'
  return uploadPhases[task.uploadPhase ?? '']
}
function actionLabel(task:Task, action:TaskAction): MessageKey {
  if (action === 'resume') return task.direction === 'upload' ? 'tasks.resumeUpload' : 'tasks.resume'
  return {pause:'tasks.pause', cancel:'tasks.cancelUpload', cleanup:'tasks.retryCleanup'}[action] as MessageKey
}
async function taskAction(task:Task, action:TaskAction, confirmed = false) {
  if (pendingTask.value || !taskActions(task).includes(action)) return
  if (action === 'cancel' && !confirmed) {
    cancelID.value = task.id
    return
  }
  cancelID.value = ''
  pendingTask.value = task.id
  taskFailure.value = undefined
  try {
    if (task.direction === 'download') {
      await (action === 'pause' ? pauseDownload(task.id) : resumeDownload(task.id))
    } else {
      const request = {pause:pauseUpload, resume:resumeUpload, cancel:cancelUpload, cleanup:cancelUpload}[action]
      await request(task.id)
    }
    await refresh('tasks')
  } catch (error) { taskFailure.value = workspaceError(error) }
  finally { pendingTask.value = '' }
}
let poll: ReturnType<typeof setInterval>
function navigate(p: Page) {
  page.value = p
  if (isDesktop && p !== 'settings') void refresh(dataPage.value)
}
onMounted(() => {
  if (!isDesktop) return
  void refresh('connections')
  poll = setInterval(() => {
    if (page.value === 'tasks' && !failures.value.tasks && !pendingTask.value && !document.hidden) void refresh('tasks')
  }, 1800)
})
onUnmounted(() => clearInterval(poll))
function edit(profile?: Profile, provider: Provider = 'aws') {
  editing.value = profile
  initialProvider.value = profile?.provider ?? provider
  showDialog.value = true
}

</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <div class="brand"><span class="brand-symbol"><Layers3 :size="25" :stroke-width="2.3" /></span><div><strong>{{ t('app.name') }}</strong><span>{{ t('app.descriptor') }}</span></div></div>
      <nav>
        <button v-for="item in nav" :key="item.id" :data-page="item.id" :aria-current="page === item.id ? 'page' : undefined" @click="navigate(item.id)">
          <component :is="item.icon" />{{ t(`nav.${item.id}`) }}
        </button>
      </nav>
      <div class="sidebar-bottom">
        <nav><button data-page="settings" :aria-current="page === 'settings' ? 'page' : undefined" @click="navigate('settings')"><Settings2 />{{ t('nav.settings') }}</button></nav>
        <LanguageSwitch compact />
        <div class="build"><span class="build-dot"></span>{{ t('workspace.version') }}</div>
      </div>
    </aside>
    <main>
      <div class="workspace"><HardDrive />{{ t(isDesktop ? 'workspace.local' : 'workspace.preview') }}</div>
      <header class="page-head"><div><h1>{{ t(`nav.${page}`) }}</h1><p>{{ t(`${page}.description`) }}</p></div>
        <button v-if="page === 'connections'" class="primary" :disabled="!isDesktop || !!failure || loading.connections" @click="edit()"><Plus />{{ t('connections.add') }}</button>
      </header>
      <div v-if="failure || (!isDesktop && page !== 'settings')" :role="failure ? 'alert' : 'status'" class="notice">
        <Info /><span>{{ t(failure?.key ?? 'error.preview', failure?.detail?.params) }}</span>
        <button v-if="failure" class="inline-button" @click="refresh(dataPage)">{{ t('action.retry') }}</button>
      </div>
      <template v-if="page === 'connections'">
        <AccountExamples :can-create="isDesktop && !failure && !loading.connections" @use="edit(undefined, $event)"/>
        <section class="panel">
          <header class="panel-head"><div class="panel-label"><Server /><h2>{{ t('connections.list') }}</h2></div>
            <span v-if="isDesktop && !failure && !loading.connections" class="count">{{ t('connections.count', {count:profiles.length}) }}</span>
          </header>
          <div v-if="profiles.length" class="tools"><label class="search"><Search /><input v-model="query" type="search" :placeholder="t('connections.search')" :aria-label="t('connections.search')"></label>
            <label class="filter"><SlidersHorizontal /><select v-model="filter" :aria-label="t('connections.provider')"><option value="">{{ t('connections.all') }}</option>
              <option v-for="p in providers" :key="p" :value="p">{{ t(`provider.${p}`) }}</option></select></label>
          </div>
          <div v-if="visible.length && !failure" class="table-scroll"><table><thead><tr>
            <th>{{ t('connections.name') }}</th><th>{{ t('connections.provider') }}</th><th>{{ t('connections.region') }}</th><th>{{ t('connections.endpoint') }}</th><th><span class="sr-only">{{ t('connections.actions') }}</span></th>
          </tr></thead><tbody v-if="!failure"><tr v-for="p in visible" :key="p.id">
            <td class="strong">{{ p.name }}</td><td>{{ t(`provider.${p.provider}`) }}<small v-if="!supportsCloud(p)" class="account-capability">{{ t('connections.localOnly') }}</small></td><td>{{ p.region || t('connections.default') }}</td><td>{{ p.endpoint || t('connections.default') }}</td>
            <td><button class="inline-button account-edit" :disabled="loading.connections" :aria-label="t('connections.actionEdit', {name:p.name})" @click="edit(p)"><Pencil />{{ t('connections.edit') }}</button></td>
          </tr></tbody></table></div>
          <div v-if="failure || loading.connections || !visible.length" class="empty">
            <div class="empty-icon"><Waypoints /></div>
            <template v-if="loading.connections"><h3>{{ t('common.loading') }}</h3></template>
            <template v-else-if="failure || !isDesktop"><h3>{{ t('connections.list') }}</h3><p>{{ t('connections.emptyText') }}</p></template>
            <template v-else><h3>{{ t(profiles.length ? 'connections.noMatch' : 'connections.empty') }}</h3><p>{{ t(profiles.length ? 'connections.noMatchText' : 'connections.emptyText') }}</p></template>
          </div>
        </section>
      </template>
      <template v-else-if="page === 'objects'">
        <ObjectBrowser :unavailable="!!failure" @manage="navigate('connections')" @tasks="navigate('tasks')" @credentials="edit($event)"/>
      </template>
      <template v-else-if="page === 'tasks'">
        <section class="panel">
          <header class="panel-head"><div class="panel-label"><Activity /><h2>{{ t('tasks.title') }}</h2></div></header>
          <ErrorNotice v-if="taskFailure" :error="taskFailure"/>
          <div class="table-scroll"><table><thead><tr><th>{{ t('tasks.object') }}</th><th>{{ t('tasks.direction') }}</th><th>{{ t('tasks.state') }}</th><th>{{ t('tasks.progress') }}</th><th>{{ t('connections.actions') }}</th></tr></thead>
            <tbody v-if="!failure">
              <tr v-for="task in tasks" :key="task.id"><td class="object-key">{{ task.bucket }}/{{ task.objectKey }}</td>
                <td>{{ t(task.direction === 'upload' ? 'tasks.upload' : task.direction === 'download' ? 'tasks.download' : 'tasks.unknown') }}</td>
                <td>{{ t(states[task.state] ?? 'tasks.unknown') }}<small v-if="phaseLabel(task)" class="task-phase">{{ t(phaseLabel(task)!) }}</small><ErrorNotice v-if="task.lastError" :error="localizedError(task.lastError)"/></td>
                <td>{{ t('tasks.bytes', {done:(task.transferredBytes ?? 0).toLocaleString(locale), total:task.size.toLocaleString(locale)}) }}</td>
                <td><div class="task-actions">
                  <button v-for="action in taskActions(task)" :key="action" class="inline-button" :class="{destructive:action === 'cancel'}"
                    :disabled="!!pendingTask" @click="taskAction(task,action)">{{ t(actionLabel(task,action)) }}</button>
                </div></td>
              </tr></tbody></table></div>
          <div v-if="failure || !tasks.length" class="empty"><div class="empty-icon"><ArrowDownUp /></div>
            <h3>{{ t(!isDesktop || failure ? 'tasks.title' : fetchedTasks ? 'tasks.empty' : 'common.loading') }}</h3><p>{{ t('tasks.emptyText') }}</p></div>
        </section>
      </template>
      <template v-else>
        <LanguageSettings />
      </template>
    </main>
    <CancelUploadDialog v-if="cancelTarget" :task="cancelTarget"
      :connection="profiles.find(item => item.id === cancelTarget?.connectionId)?.name ?? cancelTarget.connectionId"
      @close="cancelID = ''" @confirm="taskAction(cancelTarget,'cancel',true)"/>
    <ProfileDialog v-if="showDialog" :profile="editing" :initial-provider="initialProvider" @close="showDialog = false"/>
  </div>
</template>
