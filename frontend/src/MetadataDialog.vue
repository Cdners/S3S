<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { Copy, X } from '@lucide/vue'
import { t, locale } from './i18n'
import ErrorNotice from './ErrorNotice.vue'
import { copyObjectKey, objectMetadata, workspaceError, type Profile, type ObjectMetadata, type WorkspaceError } from './workspace'
const props = defineProps<{profile:Profile; bucket:string; objectKey:string}>()
const emit = defineEmits<{ close:[] }>()
const dialog = ref<HTMLDialogElement>()
const metadata = ref<ObjectMetadata>()
const failure = ref<WorkspaceError>()
const copied = ref<'ok' | 'failed'>()
const busy = ref(false)
const lifetime = new AbortController()
async function read() {
  if (busy.value) return
  busy.value = true
  failure.value = undefined
  try {
    const data = await objectMetadata(props.profile,props.bucket,props.objectKey,lifetime.signal)
    if (!lifetime.signal.aborted) metadata.value = data
  } catch (error) {
    if (!lifetime.signal.aborted) failure.value = workspaceError(error)
  } finally { busy.value = false }
}
async function copy() {
  try { await copyObjectKey(props.objectKey); copied.value = 'ok' }
  catch { copied.value = 'failed' }
}
onMounted(() => { dialog.value?.showModal(); void read() })
onBeforeUnmount(() => lifetime.abort())
</script>
<template>
  <dialog ref="dialog" class="metadata-dialog" aria-labelledby="metadata-title" @cancel.prevent="emit('close')">
    <header class="dialog-header"><h2 id="metadata-title">{{ t('objects.details') }}</h2>
      <button type="button" class="icon-button" :aria-label="t('action.close')" @click="emit('close')"><X/></button></header>
    <div class="metadata-body">
      <dl><dt>{{ t('objects.bucketName') }}</dt><dd>{{ bucket }}</dd>
        <dt>{{ t('objects.key') }}</dt><dd class="object-key">{{ objectKey }}</dd>
        <template v-if="metadata"><dt>{{ t('objects.size') }}</dt><dd>{{ t('objects.bytes', {value:metadata.size.toLocaleString(locale)}) }}</dd>
          <template v-if="metadata.etag"><dt>{{ t('objects.etag') }}</dt><dd>{{ metadata.etag }}</dd></template>
          <template v-if="metadata.versionId"><dt>{{ t('objects.versionId') }}</dt><dd>{{ metadata.versionId }}</dd></template>
        </template>
      </dl>
      <p v-if="busy" role="status">{{ t('common.loading') }}</p>
      <ErrorNotice v-if="failure" :error="failure"/>
      <button v-if="failure" class="secondary" @click="read">{{ t('action.retry') }}</button>
      <p v-if="copied" role="status">{{ t(copied === 'ok' ? 'objects.copied' : 'objects.copyFailed') }}</p>
    </div>
    <footer class="dialog-footer"><button class="secondary" data-copy-key @click="copy"><Copy/>{{ t('objects.copyKey') }}</button>
      <button class="primary" @click="emit('close')">{{ t('action.close') }}</button></footer>
  </dialog>
</template>
