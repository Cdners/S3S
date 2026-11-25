<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { KeyRound, X } from '@lucide/vue'
import ErrorNotice from './ErrorNotice.vue'
import { t } from './i18n'
import { forgetCredentials, setCredentials, supportsCloud, workspaceError, type Profile, WorkspaceError } from './workspace'

const props = defineProps<{ profile: Profile }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const dialog = ref<HTMLDialogElement>()
const form = ref<HTMLFormElement>()
const access = ref<HTMLInputElement>()
const secret = ref<HTMLInputElement>()
const token = ref<HTMLInputElement>()
const saving = ref(false)
const confirmRemoval = ref(!supportsCloud(props.profile))
const failure = ref<WorkspaceError>()
onMounted(() => dialog.value?.showModal())
onUnmounted(() => form.value?.reset())
function close() {
  if (saving.value) return
  form.value?.reset()
  emit('close')
}
async function save() {
  if (saving.value || !access.value || !secret.value || !token.value) return
  if (!access.value.value || !secret.value.value) {
    form.value?.reset()
    failure.value = new WorkspaceError('error.invalidCredentials')
    return
  }
  // Keep secret values out of Vue state, navigation, storage and errors.
  const input = {accessKeyId: access.value.value, secretAccessKey: secret.value.value, sessionToken: token.value.value}
  form.value?.reset()
  saving.value = true
  failure.value = undefined
  try {
    await setCredentials(props.profile, input)
    emit('saved')
  } catch (error) { failure.value = workspaceError(error) }
  finally {
    input.accessKeyId = input.secretAccessKey = input.sessionToken = ''
    saving.value = false
  }
}
async function forget() {
  if (saving.value) return
  form.value?.reset()
  if (!confirmRemoval.value) {
    confirmRemoval.value = true
    failure.value = undefined
    return
  }
  saving.value = true
  try { await forgetCredentials(props.profile); emit('saved') }
  catch (error) { failure.value = workspaceError(error) }
  finally { saving.value = false }
}
</script>
<template>
  <dialog ref="dialog" aria-labelledby="creds-title" @cancel.prevent="close">
    <header class="dialog-header"><KeyRound/><h2 id="creds-title">{{ t(confirmRemoval ? 'credentials.forgetNamed' : 'credentials.title', {name:profile.name}) }}</h2>
      <button type="button" class="icon-button" :aria-label="t('action.close')" :disabled="saving" @click="close"><X/></button></header>
    <form ref="form" novalidate autocomplete="off" @submit.prevent="save">
      <fieldset v-if="!confirmRemoval" :disabled="saving">
        <p class="hint">{{ t('credentials.hint') }}</p>
        <p class="scope" dir="auto">{{ t(`provider.${profile.provider}`) }} · {{ profile.endpoint || t('connections.default') }}</p>
        <label for="credential-access">{{ t('credentials.access') }}</label>
        <input id="credential-access" ref="access" type="password" autocomplete="off" spellcheck="false" required autofocus>
        <label for="credential-secret">{{ t('credentials.secret') }}</label>
        <input id="credential-secret" ref="secret" type="password" autocomplete="off" spellcheck="false" required>
        <label for="credential-token">{{ t('credentials.token') }}</label>
        <input id="credential-token" ref="token" type="password" autocomplete="off" spellcheck="false">
      </fieldset>
      <p v-else class="remove-prompt">{{ t('credentials.forgetPrompt', {name:profile.name}) }}</p>
      <ErrorNotice v-if="failure" :error="failure" />
      <footer class="dialog-footer credentials-footer">
        <button type="button" class="inline-button" :disabled="saving" @click="forget">{{ t(confirmRemoval ? 'credentials.confirmForget' : 'credentials.forget') }}</button>
        <button type="button" class="secondary" :disabled="saving" @click="close">{{ t('action.cancel') }}</button>
        <button v-if="!confirmRemoval" type="submit" class="primary" :disabled="saving">{{ t('credentials.save') }}</button></footer>
    </form>
  </dialog>
</template>
