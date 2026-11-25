<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  Activity, ArrowDownUp, ArrowRight, Box, CircleAlert, FileUp,
  FolderClosed, FolderOpen, HardDrive, Info, Languages, Layers3, Pencil,
  Plus, Search, Server, Settings2, SlidersHorizontal, Waypoints
} from '@lucide/vue'
import ProfileDialog from './ProfileDialog.vue'
import { importLanguage, languages, locale, setLanguage, t, type MessageKey } from './i18n'
import { errorKey, isDesktop, loadProfiles, loadTasks, profiles, providers, tasks, type Profile, type Provider } from './workspace'

type Page = 'connections' | 'objects' | 'tasks' | 'settings'
const nav = [{ id:'connections', icon:Waypoints }, { id:'objects', icon:FolderClosed }, { id:'tasks', icon:ArrowDownUp }] as const
const page = ref<Page>('connections')
const query = ref('')
const filter = ref<Provider | ''>('')
const selectedId = ref('')
const editing = ref<Profile>()
const showDialog = ref(false)
const importing = ref(false)
const importAlert = ref<MessageKey>()
const fileInput = ref<HTMLInputElement>()
const failures = ref<Partial<Record<'connections' | 'tasks', MessageKey>>>({})
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
  try { await (kind === 'connections' ? loadProfiles() : loadTasks()) }
  catch (error) { failures.value[kind] = errorKey(error) }
  finally { loading.value[kind] = false }
}
function navigate(p: Page) {
  page.value = p
  if (isDesktop && p !== 'settings') void refresh(dataPage.value)
}
onMounted(() => { if (isDesktop) void refresh('connections') })
function edit(profile?: Profile) { editing.value = profile; showDialog.value = true }
async function importFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file || importing.value) return
  importing.value = true
  try { await importLanguage(file); importAlert.value = 'settings.imported' }
  catch { importAlert.value = 'settings.importError' }
  finally { importing.value = false; input.value = '' }
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
        <div class="build"><span class="build-dot"></span>{{ t('workspace.version') }}</div>
      </div>
    </aside>
    <main>
      <div class="workspace"><HardDrive />{{ t(isDesktop ? 'workspace.local' : 'workspace.preview') }}</div>
      <header class="page-head"><div><h1>{{ t(`nav.${page}`) }}</h1><p>{{ t(`${page}.description`) }}</p></div>
        <button v-if="page === 'connections'" class="primary" :disabled="!isDesktop || !!failure" @click="edit()"><Plus />{{ t('connections.add') }}</button>
      </header>
      <div v-if="failure || (!isDesktop && page !== 'settings')" :role="failure ? 'alert' : 'status'" class="notice">
        <Info /><span>{{ t(failure ?? 'error.preview') }}</span>
        <button v-if="failure" class="inline-button" @click="refresh(dataPage)">{{ t('action.retry') }}</button>
      </div>
      <template v-if="page === 'connections'">
        <section class="panel">
          <header class="panel-head"><div class="panel-label"><Server /><h2>{{ t('connections.list') }}</h2></div>
            <span v-if="isDesktop && !failure && !loading.connections" class="count">{{ t('connections.count', {count:profiles.length}) }}</span>
          </header>
          <div class="tools"><label class="search"><Search /><input v-model="query" type="search" :placeholder="t('connections.search')" :aria-label="t('connections.search')"></label>
            <label class="filter"><SlidersHorizontal /><select v-model="filter" :aria-label="t('connections.provider')"><option value="">{{ t('connections.all') }}</option>
              <option v-for="p in providers" :key="p" :value="p">{{ t(`provider.${p}`) }}</option></select></label>
          </div>
          <div class="table-scroll"><table><thead><tr>
            <th>{{ t('connections.name') }}</th><th>{{ t('connections.provider') }}</th><th>{{ t('connections.region') }}</th><th>{{ t('connections.endpoint') }}</th><th><span class="sr-only">{{ t('connections.actions') }}</span></th>
          </tr></thead><tbody v-if="!failure"><tr v-for="p in visible" :key="p.id">
            <td class="strong">{{ p.name }}</td><td>{{ t(`provider.${p.provider}`) }}</td><td>{{ p.region || t('connections.default') }}</td><td>{{ p.endpoint || t('connections.default') }}</td>
            <td><button class="icon-button" :aria-label="t('connections.actionEdit', {name:p.name})" @click="edit(p)"><Pencil /></button></td>
          </tr></tbody></table></div>
          <div v-if="failure || loading.connections || !visible.length" class="empty">
            <div class="empty-icon"><Waypoints /></div>
            <template v-if="loading.connections"><h3>{{ t('common.loading') }}</h3></template>
            <template v-else-if="failure || !isDesktop"><h3>{{ t('connections.list') }}</h3><p>{{ t('connections.emptyText') }}</p></template>
            <template v-else><h3>{{ t(profiles.length ? 'connections.noMatch' : 'connections.empty') }}</h3><p>{{ t(profiles.length ? 'connections.noMatchText' : 'connections.emptyText') }}</p></template>
          </div>
        </section>
        <p class="footnote"><Info />{{ t('connections.noCloud') }}</p>
      </template>
      <template v-else-if="page === 'objects'">
        <section class="panel">
          <header class="panel-head"><div class="panel-label"><FolderOpen /><h2>{{ t('objects.title') }}</h2></div>
            <select v-model="selectedId" :aria-label="t('objects.connection')" :disabled="!profiles.length || !!failure || !isDesktop"><option value="">{{ t('objects.select') }}</option>
              <option v-for="p in profiles" :key="p.id" :value="p.id">{{ p.name }}</option></select>
          </header>
          <table><thead><tr><th>{{ t('objects.key') }}</th><th>{{ t('objects.size') }}</th><th>{{ t('objects.modified') }}</th></tr></thead></table>
          <div class="empty"><div class="empty-icon"><Box /></div><h3>{{ t('objects.empty') }}</h3><p>{{ t('objects.emptyText') }}</p>
          <button class="inline-button" @click="navigate('connections')">{{ t('objects.manage') }}<ArrowRight /></button></div>
        </section>
      </template>
      <template v-else-if="page === 'tasks'">
        <section class="panel">
          <header class="panel-head"><div class="panel-label"><Activity /><h2>{{ t('tasks.title') }}</h2></div></header>
          <div class="table-scroll"><table><thead><tr><th>{{ t('tasks.object') }}</th><th>{{ t('tasks.direction') }}</th><th>{{ t('tasks.state') }}</th></tr></thead>
            <tbody v-if="!failure"><tr v-for="task in tasks" :key="task.id"><td class="object-key">{{ task.bucket }}/{{ task.objectKey }}</td><td>{{ t(task.direction === 'upload' ? 'tasks.upload' : task.direction === 'download' ? 'tasks.download' : 'tasks.unknown') }}</td><td>{{ t(states[task.state] ?? 'tasks.unknown') }}</td></tr></tbody></table></div>
          <div v-if="failure || loading.tasks || !tasks.length" class="empty"><div class="empty-icon"><ArrowDownUp /></div>
            <h3>{{ t(loading.tasks ? 'common.loading' : isDesktop && !failure ? 'tasks.empty' : 'tasks.title') }}</h3><p>{{ t('tasks.emptyText') }}</p></div>
        </section>
      </template>
      <template v-else>
        <section class="panel settings-panel"><header class="panel-head"><div class="panel-label"><Languages /><h2>{{ t('settings.appearance') }}</h2></div></header>
          <div class="setting"><div><label for="language">{{ t('settings.language') }}</label><p>{{ t('settings.languageHint') }}</p></div>
            <select id="language" :value="locale" @change="setLanguage(($event.target as HTMLSelectElement).value)">
              <option v-for="lang in languages" :key="lang.locale" :value="lang.locale">{{ lang.name }}</option></select></div>
          <div class="setting"><div><h3>{{ t('settings.packs') }}</h3><p>{{ t('settings.importHelp') }}</p></div>
            <button class="secondary" :disabled="importing" @click="fileInput?.click()"><FileUp />{{ t('settings.import') }}</button>
            <input id="language-file" ref="fileInput" type="file" accept=".json,application/json" hidden @change="importFile"></div>
          <p v-if="importAlert" class="import-status" role="status"><CircleAlert />{{ t(importAlert) }}</p>
        </section>
      </template>
    </main>
    <ProfileDialog v-if="showDialog" :profile="editing" @close="showDialog = false"/>
  </div>
</template>
