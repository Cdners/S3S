<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { t } from './i18n'
import type { Task } from './workspace'

defineProps<{ task:Task; connection:string }>()
const emit = defineEmits<{ close:[]; confirm:[] }>()
const dialog = ref<HTMLDialogElement>()
onMounted(() => dialog.value?.showModal())
</script>
<template>
  <dialog ref="dialog" aria-labelledby="cancel-upload-title" @cancel.prevent="emit('close')">
    <header class="dialog-header"><h2 id="cancel-upload-title">{{ t('tasks.cancelUpload') }}</h2></header>
    <div class="cancel-summary">
      <dl class="upload-target">
        <dt>{{ t('objects.connection') }}</dt><dd>{{ connection }}</dd>
        <dt>{{ t('objects.bucketName') }}</dt><dd>{{ task.bucket }}</dd>
        <dt>{{ t('objects.key') }}</dt><dd class="object-key">{{ task.objectKey }}</dd>
      </dl>
      <p>{{ t('upload.cancelPrompt') }}</p>
    </div>
    <footer class="dialog-footer">
      <button type="button" autofocus class="secondary" @click="emit('close')">{{ t('upload.keep') }}</button>
      <button type="button" class="danger" @click="emit('confirm')">{{ t('upload.confirmCancel') }}</button>
    </footer>
  </dialog>
</template>
