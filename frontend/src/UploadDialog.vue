<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { FileUp, X } from '@lucide/vue'
import { t } from './i18n'
import ErrorNotice from './ErrorNotice.vue'
import { displayKey, editKey, replaceKeySelection, validKey } from './objectKey'
import { startUpload, workspaceError, type Profile, type WorkspaceError } from './workspace'

const props = defineProps<{profile:Profile; bucket:string; prefix:string}>()
const emit = defineEmits<{close:[]; enqueued:[]; cancelPreparation:[]}>()
const dialog = ref<HTMLDialogElement>()
const key = ref(props.prefix)
const preparation = shallowRef<AbortController>()
const failure = ref<WorkspaceError>()
onMounted(() => dialog.value?.showModal())

onBeforeUnmount(() => preparation.value?.abort())
function close() {
  if (preparation.value) {
    preparation.value.abort()
    emit('cancelPreparation')
  } else {
    emit('close')
  }
}
async function pasteKey(event: ClipboardEvent) {
  const text = event.clipboardData?.getData('text/plain')
  if (!text?.includes('\r')) return
  event.preventDefault()
  const editor = event.target as HTMLTextAreaElement
  const start = editor.selectionStart
  key.value = replaceKeySelection(key.value, start, editor.selectionEnd, text)
  await nextTick()
  const caret = start + displayKey(text).length
  editor.setSelectionRange(caret,caret)
}
async function chooseFile() {
  if (preparation.value || !validKey(key.value)) return
  const controller = new AbortController()
  preparation.value = controller
  failure.value = undefined
  try {
    // Only the destination crosses IPC. The backend opens the system picker
    // and owns the selected path, file identity, contents and checkpoints.
    const id = await startUpload(props.profile, props.bucket, key.value, t('native.openTitle'), controller.signal)
    if (id && !controller.signal.aborted) emit('enqueued')
  } catch (error) {
    if (!controller.signal.aborted) failure.value = workspaceError(error)
  } finally {
    preparation.value = undefined
  }
}
</script>
<template>
  <dialog ref="dialog" aria-labelledby="upload-title" @cancel.prevent="close">
    <header class="dialog-header"><span class="square-icon"><FileUp/></span>
      <h2 id="upload-title">{{ t('objects.upload') }}</h2>
      <button class="icon-button" :aria-label="t('action.close')" @click="close"><X/></button>
    </header>
    <form novalidate @submit.prevent="chooseFile">
      <fieldset :disabled="!!preparation">
        <dl class="upload-target">
          <dt>{{ t('objects.connection') }}</dt><dd>{{ profile.name }}</dd>
          <dt>{{ t('objects.bucketName') }}</dt><dd>{{ bucket }}</dd>
        </dl>
        <label for="upload-key">{{ t('upload.key') }}</label>
        <textarea id="upload-key" :value="displayKey(key)" @input="key = editKey(key, ($event.target as HTMLTextAreaElement).value)" @paste="pasteKey"
          rows="3" autofocus spellcheck="false" autocomplete="off" aria-describedby="upload-hint"></textarea>
        <p id="upload-hint" class="hint">{{ t('upload.keyHint') }}</p>
        <p v-if="key && !validKey(key)" class="alert">{{ t('upload.keyLimit') }}</p>
        <p class="hint">{{ t('upload.noClobber') }}</p>
      </fieldset>
      <p v-if="preparation" class="notice" role="status">{{ t('upload.preparing') }}</p>
      <ErrorNotice v-if="failure" :error="failure"/>
      <footer class="dialog-footer"><button type="button" class="secondary" @click="close">{{ t(preparation ? 'upload.stopPreparation' : 'action.cancel') }}</button>
        <button type="submit" class="primary" :disabled="!!preparation || !validKey(key)">{{ t('upload.chooseFile') }}</button></footer>
    </form>
  </dialog>
</template>
