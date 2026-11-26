<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  Activity, ArrowDownUp, Cable,
  FolderClosed, HardDrive, Info, Layers3, Pencil,
  Plus, Search, Server, Settings2, SlidersHorizontal, Waypoints
} from '@lucide/vue'
import ProfileDialog from './ProfileDialog.vue'
import AccountExamples from './AccountExamples.vue'
import ObjectBrowser from './ObjectBrowser.vue'
import ErrorNotice from './ErrorNotice.vue'
import ProbeFeedback from './ProbeFeedback.vue'
import { useConnectionProbe } from './connectionProbe'
import CancelUploadDialog from './CancelUploadDialog.vue'
import { taskActions, type TaskAction } from './transferActions'
import LanguageSwitch from './LanguageSwitch.vue'
import LanguageSettings from './LanguageSettings.vue'
import BandwidthSettings from './BandwidthSettings.vue'
import { locale, t, type MessageKey } from './i18n'
import { workspaceError, isDesktop, loadProfiles, loadTasks, profiles, providers, tasks, capabilitiesFor, loadCapabilities, pauseDownload, resumeDownload, pauseUpload, resumeUpload, cancelUpload, localizedError, type Task, type Profile, type Provider, type WorkspaceError } from './workspace'

type Page = 'connections' | 'objects' | 'tasks' | 'settings'
const nav = [{ id:'connections', icon:Waypoints }, { id:'objects', icon:FolderClosed }, { id:'tasks', icon:ArrowDownUp }] as const
const page = ref<Page>('connections')
const visitedBrowser = ref(false)
const browserTarget = ref<{id:string}>()
const { probe, run:testAccount, clear:clearProbe } = useConnectionProbe()
const capabilityError = ref<WorkspaceError>()
const capabilitiesPending = ref(isDesktop)
watch(() => profiles.value.find(p => p.id === probe.value?.profile.id), current => {
  if (probe.value && current !== probe.value.profile) clearProbe()
})
async function readCapabilities() {
  capabilitiesPending.value = true
  capabilityError.value = undefined
  try { await loadCapabilities() }
  catch (error) { capabilityError.value = workspaceError(error) }
  finally { capabilitiesPending.value = false }
}
function connect(profile: Profile) {
  browserTarget.value = {id:profile.id}
  navigate('objects')
}
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
  if (p === 'objects') visitedBrowser.value = true
  if (p !== 'connections') clearProbe()
  if (isDesktop && p !== 'settings') void refresh(dataPage.value)
}
onMounted(() => {
  if (!isDesktop) return
  void refresh('connections')
  void readCapabilities()
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
      <div v-if="capabilityError && page !== 'settings' && page !== 'tasks'" class="notice" role="alert">
        <span>{{ t('connections.capabilityError') }}</span><button class="inline-button" @click="readCapabilities">{{ t('action.retry') }}</button>
      </div>
      <section v-if="page === 'connections'" class="accounts-region" :class="{populated:profiles.length}">
        <AccountExamples :can-create="isDesktop && !failure && !loading.connections" @use="edit(undefined, $event)"/>
        <section class="panel">
          <header class="panel-head account-toolbar"><div class="panel-label"><Server /><h2>{{ t('connections.list') }}</h2></div>
          <div v-if="profiles.length" class="tools"><label class="search"><Search /><input v-model="query" type="search" :placeholder="t('connections.search')" :aria-label="t('connections.search')"></label>
            <label class="filter"><SlidersHorizontal /><select v-model="filter" :aria-label="t('connections.provider')"><option value="">{{ t('connections.all') }}</option>
              <option v-for="p in providers" :key="p" :value="p">{{ t(`provider.${p}`) }}</option></select></label>
          </div></header>
          <div v-if="visible.length && !failure" class="accounts-wrap"><table class="accounts-table"><thead><tr>
            <th>{{ t('connections.name') }}</th><th>{{ t('connections.storageService') }}</th><th>{{ t('connections.actions') }}</th>
          </tr></thead><tbody>
            <template v-for="p in visible" :key="p.id"><tr class="account-row">
              <td class="account-name">{{ p.name }}</td>
              <td class="service-info"><span>{{ t(`provider.${p.provider}`) }}</span><span v-if="p.region" class="service-region">{{ p.region }}</span>
                <small v-if="p.endpoint" dir="auto" :title="p.endpoint">{{ p.endpoint }}</small></td>
              <td class="account-action-cell"><div class="account-actions">
                <button class="primary compact-button" data-connect :aria-label="t('connections.connectNamed', {name:p.name})"
                  :disabled="!capabilitiesFor(p.provider)?.listObjects" @click="connect(p)"><Cable/>{{ t('connections.connect') }}</button>
                <button class="secondary compact-button" data-test-account :aria-label="t('connections.testNamed', {name:p.name})"
                  :disabled="!capabilitiesFor(p.provider)?.listBuckets || (probe?.profile.id === p.id && probe.status === 'pending')" @click="testAccount(p)">{{ t('connections.test') }}</button>
                <button class="icon-button" data-edit-account :aria-label="t('connections.actionEdit', {name:p.name})" :title="t('connections.edit')" @click="edit(p)"><Pencil/></button>
              </div></td></tr>
              <tr v-if="probe?.profile.id === p.id" class="account-test-row"><td colspan="3"><ProbeFeedback :test="probe"/></td></tr>
            </template>
          </tbody></table></div>
          <div v-if="failure || !visible.length" class="empty">
            <div class="empty-icon"><Waypoints /></div>
            <template v-if="loading.connections"><h3>{{ t('common.loading') }}</h3></template>
            <template v-else-if="failure || !isDesktop"><h3>{{ t('connections.list') }}</h3><p>{{ t('connections.emptyText') }}</p></template>
            <template v-else><h3>{{ t(profiles.length ? 'connections.noMatch' : 'connections.empty') }}</h3><p>{{ t(profiles.length ? 'connections.noMatchText' : 'connections.emptyText') }}</p></template>
          </div>
        </section>
      </section>
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
      <template v-else-if="page === 'settings'">
        <LanguageSettings />
        <BandwidthSettings v-if="isDesktop" class="bandwidth-panel"/>
      </template>
      <ObjectBrowser v-if="visitedBrowser" v-show="page === 'objects'" :target="browserTarget" :capabilities-pending="capabilitiesPending" :unavailable="!!failures.connections"
        @manage="navigate('connections')" @tasks="navigate('tasks')" @credentials="edit($event)"/>
    </main>
    <CancelUploadDialog v-if="cancelTarget" :task="cancelTarget"
      :connection="profiles.find(item => item.id === cancelTarget?.connectionId)?.name ?? cancelTarget.connectionId"
      @close="cancelID = ''" @confirm="taskAction(cancelTarget,'cancel',true)"/>
    <ProfileDialog v-if="showDialog" :profile="editing" :initial-provider="initialProvider" @close="showDialog = false"/>
  </div>
</template>
