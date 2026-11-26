<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Box, Download, FileUp, Folder, FolderOpen, KeyRound, RefreshCw } from '@lucide/vue'
import { t, locale } from './i18n'
import { Listing, PaginationError, parentPrefix } from './listing'
import ErrorNotice from './ErrorNotice.vue'
import UploadDialog from './UploadDialog.vue'
import ProbeFeedback from './ProbeFeedback.vue'
import MetadataDialog from './MetadataDialog.vue'
import { useConnectionProbe } from './connectionProbe'
import {
  isDesktop, listBuckets, listObjects, profiles, startDownload, capabilitiesFor,
  WorkspaceError, workspaceError, type ObjectInfo, type Profile
} from './workspace'

const emit = defineEmits<{ manage: []; tasks: []; credentials: [profile:Profile] }>()
const props = defineProps<{ unavailable: boolean; capabilitiesPending: boolean; target?: {id:string} }>()
const selectedId = ref('')
const profile = computed(() => profiles.value.find(item => item.id === selectedId.value))
const caps = computed(() => profile.value && capabilitiesFor(profile.value.provider))
const {probe, run:testAccess, clear:clearProbe} = useConnectionProbe()
const inspecting = ref<string>()
const inputBucket = ref('')
const bucket = ref('')
const prefix = ref('')
type Entry = {kind:'prefix'; name:string} | {kind:'object'; name:string; info:ObjectInfo}
const buckets = reactive(new Listing<string>())
const objects = reactive(new Listing<Entry>())
const bucketError = ref<WorkspaceError>()
const objectError = ref<WorkspaceError>()
const downloadError = ref<WorkspaceError>()
const prompting = ref(false)
const transferNotice = ref<'objects.enqueued' | 'upload.enqueued' | 'upload.stopRequested'>()
const uploadDialog = ref(false)
let scope = 0
let bucketRequest: AbortController | undefined
let objectRequest: AbortController | undefined

function resetObjectView() {
  scope++
  objectRequest?.abort()
  objectRequest = undefined
  inspecting.value = undefined
  uploadDialog.value = false
  objects.reset()
  objectError.value = undefined
  downloadError.value = undefined
  transferNotice.value = undefined
  prompting.value = false
}
function reset() {
  resetObjectView()
  clearProbe()
  bucketRequest?.abort()
  bucketRequest = undefined
  buckets.reset()
  bucketError.value = undefined
  inputBucket.value = bucket.value = prefix.value = ''
}
function connectSelected() {
  reset()
  if (!props.unavailable && caps.value?.listBuckets) void moreBuckets()
}
// The dropdown, a saved-account edit and a late Capabilities reply all resolve
// through one watcher. A second explicit Connect to the same account refreshes it.
watch([profile, () => caps.value?.listBuckets, () => props.unavailable], connectSelected)
watch(inputBucket, clearProbe, {flush:'sync'})
watch(() => props.target, target => {
  if (!target) return
  if (selectedId.value === target.id) connectSelected()
  else selectedId.value = target.id
}, {immediate:true})
onUnmounted(reset)

function pageError(err: unknown) {
  return err instanceof PaginationError ? new WorkspaceError('error.pagination') : workspaceError(err)
}
async function moreBuckets() {
  const conn = profile.value
  if (!conn || !caps.value?.listBuckets) return
  bucketError.value = undefined
  try {
    await buckets.load(async token => {
      const active = new AbortController()
      bucketRequest = active
      try {
        const result = await listBuckets(conn, token, active.signal)
        return {entries:result.buckets, nextToken:result.nextToken}
      } finally { if (bucketRequest === active) bucketRequest = undefined }
    })
  } catch (error) { bucketError.value = pageError(error) }
}
async function moreObjects() {
  const conn = profile.value
  if (!conn || !caps.value?.listObjects || !bucket.value) return
  const path = { bucket:bucket.value, prefix:prefix.value }
  objectError.value = undefined
  try {
    await objects.load(async token => {
      const active = new AbortController()
      objectRequest = active
      try {
        const page = await listObjects(conn, path.bucket, path.prefix, token, active.signal)
        const entries: Entry[] = [
          ...page.prefixes.map(name => ({kind:'prefix' as const, name})),
          ...page.objects.map(info => ({kind:'object' as const, name:info.key, info}))
        ]
        return {entries, nextToken:page.nextToken}
      } finally { if (objectRequest === active) objectRequest = undefined }
    })
  } catch (error) { objectError.value = pageError(error) }
}
function open(bucketName:string, newPrefix = '') {
  if (!bucketName || !caps.value?.listObjects) return
  // A bucket-scoped identity can work even while account enumeration is pending.
  if (buckets.loading) {
    bucketRequest?.abort()
    buckets.reset()
  }
  bucketError.value = undefined
  resetObjectView()
  bucket.value = inputBucket.value = bucketName
  prefix.value = newPrefix
  void moreObjects()
}
async function download(key:string) {
  const conn = profile.value
  if (!conn || !caps.value?.download || prompting.value) return
  const generation = scope
  prompting.value = true
  transferNotice.value = undefined
  downloadError.value = undefined
  try {
    const id = await startDownload(conn, bucket.value, key, t('native.saveTitle'))
    if (generation === scope) transferNotice.value = id ? 'objects.enqueued' : undefined
  } catch (error) {
    if (generation === scope) downloadError.value = workspaceError(error)
  } finally {
    if (generation === scope) prompting.value = false
  }
}
</script>

<template>
  <section class="panel browser-panel">
    <header class="panel-head"><div class="panel-label"><FolderOpen/><h2>{{ t('objects.title') }}</h2></div>
      <select id="cloud-profile" v-model="selectedId" :aria-label="t('objects.connection')" :disabled="!profiles.length || unavailable || !isDesktop">
        <option value="">{{ t('objects.select') }}</option><option v-for="item in profiles" :key="item.id" :value="item.id">{{ item.name }}</option></select></header>
    <div v-if="profile && !caps && capabilitiesPending" class="empty" role="status">{{ t('common.loading') }}</div>
    <div v-else-if="!profile || !caps?.listObjects || unavailable" class="empty">
      <div class="empty-icon"><Box/></div><h3>{{ t(profile && !caps?.listObjects ? 'objects.unsupported' : 'objects.empty') }}</h3>
      <p>{{ profile && !caps?.listObjects ? t('objects.localOnly', {provider:t(`provider.${profile.provider}`)}) : t('objects.emptyText') }}</p><button class="inline-button" @click="emit('manage')">{{ t('objects.manage') }}<ArrowRight/></button>
    </div>
    <div v-else class="browser-content">
      <div class="browse-actions">
        <button class="secondary" :disabled="!caps?.listBuckets || buckets.loading || unavailable" @click="buckets.reset(); moreBuckets()">{{ t('objects.loadBuckets') }}</button>
        <button class="inline-button" @click="emit('credentials', profile)"><KeyRound/>{{ t('credentials.configure') }}</button>
      </div>
      <form class="bucket-form" @submit.prevent="open(inputBucket)">
        <label for="bucket-input">{{ t('objects.bucketName') }}</label>
        <div class="bucket-controls"><input id="bucket-input" v-model="inputBucket" spellcheck="false" autocomplete="off">
          <button type="submit" class="primary" :disabled="!inputBucket || unavailable || !caps?.listObjects">{{ t('objects.openBucket') }}</button>
          <button type="button" class="secondary" data-test-bucket :disabled="unavailable || probe?.status === 'pending' || !(inputBucket ? caps?.listObjects : caps?.listBuckets)"
            @click="testAccess(profile,inputBucket)">{{ t(inputBucket ? 'probe.testBucket' : 'connections.test') }}</button>
        </div><p class="hint">{{ t('objects.bucketHint') }}</p>
      </form>
      <ProbeFeedback v-if="probe" :test="probe"/>
      <p v-if="bucketError" class="scope-note">{{ t('probe.scopeAccount', {name:profile.name}) }}</p>
      <ErrorNotice v-if="bucketError" :error="bucketError"/>
      <p v-if="bucketError?.key === 'error.cloudAccess'" class="scope-note">{{ t('probe.bucketAlternative') }}</p>
      <div v-if="buckets.loaded || buckets.loading" class="bucket-results">
        <h3>{{ t('objects.buckets') }}</h3>
        <p v-if="buckets.loaded" class="scope-note" role="status">{{ t('objects.accountListed',{name:profile.name}) }}</p>
        <p v-if="buckets.loaded && !buckets.entries.length">{{ t('objects.noBuckets') }}</p>
        <div class="bucket-links"><button v-for="name in buckets.entries" :key="name" class="secondary" @click="open(name)"><Box/>{{ name }}</button></div>
        <p v-if="buckets.loading" role="status">{{ t('common.loading') }}</p>
        <button v-else-if="buckets.nextToken" class="inline-button" @click="moreBuckets">{{ t('objects.moreBuckets') }}</button>
      </div>
      <section v-if="bucket" class="remote-list">
        <div class="location"><div><h3>{{ bucket }}</h3><span class="object-key">{{ prefix || t('objects.root') }}</span></div>
          <div class="browse-actions">
            <button v-if="caps?.upload" class="primary" :disabled="prompting || unavailable" @click="uploadDialog = true"><FileUp/>{{ t('objects.upload') }}</button>
            <button v-if="prefix" class="secondary" @click="open(bucket, parentPrefix(prefix))"><ArrowLeft/>{{ t('objects.up') }}</button>
          <button class="icon-button" :aria-label="t('objects.refresh')" @click="open(bucket,prefix)"><RefreshCw/></button></div></div>
        <p v-if="caps?.download && !caps.upload" class="scope-note">{{ t('account.readOnly') }}</p>
        <ErrorNotice v-if="objectError" :error="objectError"/>
        <ErrorNotice v-if="downloadError" :error="downloadError"/>
        <p v-if="transferNotice" role="status" class="notice">{{ t(transferNotice) }} <button class="inline-button" @click="emit('tasks')">{{ t('nav.tasks') }}</button></p>
        <div class="table-scroll"><table><thead><tr><th>{{ t('objects.key') }}</th><th>{{ t('objects.size') }}</th><th>{{ t('connections.actions') }}</th></tr></thead>
          <tbody><tr v-for="item in objects.entries" :key="item.kind+':'+item.name">
            <td><button v-if="item.kind === 'prefix'" class="inline-button object-key" @click="open(bucket,item.name)"><Folder/>{{ item.name }}</button>
              <span v-else class="object-key">{{ item.name }}</span></td>
            <td>{{ item.kind === 'object' ? t('objects.bytes', {value:item.info.size.toLocaleString(locale)}) : '' }}</td>
            <td><div v-if="item.kind === 'object'" class="object-actions">
              <button v-if="caps?.metadata" class="inline-button" data-object-details @click="inspecting = item.name">{{ t('objects.details') }}</button>
              <button v-if="caps?.download" class="inline-button" :disabled="prompting" @click="download(item.name)"><Download/>{{ t('objects.download') }}</button>
            </div></td></tr></tbody>
        </table></div>
        <div class="page-status" aria-live="polite"><span v-if="objects.loading">{{ t('common.loading') }}</span>
          <span v-else-if="objects.loaded && !objects.entries.length">{{ t('objects.noObjects') }}</span>
          <button v-if="!objects.loading && (objects.nextToken || objectError)" class="inline-button" @click="moreObjects">{{ t(objectError ? 'action.retry' : 'objects.moreObjects') }}</button></div>
        <MetadataDialog v-if="inspecting !== undefined && profile" :profile="profile" :bucket="bucket" :object-key="inspecting" @close="inspecting = undefined"/>
        <UploadDialog v-if="uploadDialog && profile && caps?.upload" :profile="profile" :bucket="bucket" :prefix="prefix"
          @close="uploadDialog = false" @enqueued="uploadDialog = false; transferNotice = 'upload.enqueued'"
          @cancel-preparation="uploadDialog = false; transferNotice = 'upload.stopRequested'"/>
      </section>
    </div>
  </section>
</template>
