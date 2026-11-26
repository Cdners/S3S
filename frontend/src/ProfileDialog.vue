<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Layers3, X } from '@lucide/vue'
import ErrorNotice from './ErrorNotice.vue'
import { t } from './i18n'
import { providerGuides } from './providerGuides'
import {
  forgetCredentials, providers, saveAccount, capabilitiesFor, workspaceError, WorkspaceError,
  type Profile, type Provider, type SecretInput
} from './workspace'

const props = defineProps<{ profile?: Profile; initialProvider?: Provider }>()
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement>()
const alertArea = ref<HTMLDivElement>()
const saved = ref<Profile | null>(props.profile ?? null)
const name = ref(saved.value?.name ?? '')
const provider = ref<Provider>(saved.value?.provider ?? props.initialProvider ?? 'aws')
const region = ref(saved.value?.region ?? '')
const endpoint = ref(saved.value?.endpoint ?? '')
const access = ref<HTMLInputElement>()
const secret = ref<HTMLInputElement>()
const token = ref<HTMLInputElement>()
const busy = ref(false)
const partial = ref(false)
const confirmForget = ref(false)
const removed = ref(false)
const clearedForScope = ref(false)
const credentialsEntered = ref(false)
const failure = ref<WorkspaceError>()
const guide = computed(() => providerGuides[provider.value])
const newScope = computed(() => saved.value && (saved.value.provider !== provider.value ||
  saved.value.region !== region.value.trim() || saved.value.endpoint !== endpoint.value.trim()))
const canApplyExample = computed(() => ['aws','oss','tos'].includes(provider.value) &&
  !region.value && !endpoint.value && !credentialsEntered.value)
const validName = computed(() => name.value.trim().length > 0)

onMounted(() => dialog.value?.showModal())
onBeforeUnmount(clearSecrets)
watch(provider, () => {
  region.value = ''
  endpoint.value = ''
  scopeEdited()
}, {flush:'sync'})
watch([region,endpoint], scopeEdited, {flush:'sync'})

function recordSecretPresence() {
  credentialsEntered.value = !!(access.value?.value || secret.value?.value || token.value?.value)
}
function applyExample() {
  // OS/browser autofill might not have emitted an input event.
  recordSecretPresence()
  if (!canApplyExample.value) return
  region.value = guide.value.region
  endpoint.value = guide.value.endpoint
}
function clearSecrets() {
  credentialsEntered.value = false
  for (const field of [access.value, secret.value, token.value]) {
    if (field) field.value = ''
  }
}
function scopeEdited() {
  if (access.value?.value || secret.value?.value || token.value?.value) clearedForScope.value = true
  clearSecrets()
  failure.value = undefined
}
function close() {
  if (busy.value) return
  clearSecrets()
  emit('close')
}
async function revealError(error:WorkspaceError) {
  failure.value = error
  await nextTick()
  alertArea.value?.scrollIntoView({block:'nearest'})
}
async function save() {
  if (busy.value || !validName.value) return
  const accessKeyId = access.value?.value ?? ''
  const secretAccessKey = secret.value?.value ?? ''
  const sessionToken = token.value?.value ?? ''
  const credentials: SecretInput | null = accessKeyId || secretAccessKey || sessionToken
    ? { accessKeyId, secretAccessKey, sessionToken } : null
  if (credentials && (!accessKeyId.trim() || !secretAccessKey.trim())) {
    await revealError(new WorkspaceError('error.invalidCredentials'))
    return
  }
  if (partial.value && !credentials) {
    await revealError(new WorkspaceError('account.retryCredentials'))
    return
  }
  clearSecrets()
  failure.value = undefined
  busy.value = true
  try {
    const changes = {
      id:saved.value?.id ?? '', name:name.value.trim(), provider:provider.value,
      region:region.value.trim(), endpoint:endpoint.value.trim()
    }
    const result = await saveAccount(saved.value, changes, credentials)
    saved.value = result.profile
    partial.value = !!result.credentialError
    if (result.credentialError) await revealError(result.credentialError)
    else emit('close')
  } catch (error) { await revealError(workspaceError(error)) }
  finally {
    if (credentials) credentials.accessKeyId = credentials.secretAccessKey = credentials.sessionToken = ''
    busy.value = false
  }
}
function requestForget() {
  clearSecrets()
  failure.value = undefined
  confirmForget.value = true
}
async function forget() {
  if (busy.value || !confirmForget.value || !saved.value) return
  busy.value = true
  try {
    await forgetCredentials(saved.value)
    partial.value = false
    confirmForget.value = false
    removed.value = true
    failure.value = undefined
  } catch (error) { await revealError(workspaceError(error)) }
  finally { busy.value = false }
}
</script>

<template>
  <dialog ref="dialog" class="account-dialog" aria-labelledby="profile-title" @cancel.prevent="close">
    <header class="dialog-header">
      <span class="square-icon"><Layers3 /></span>
      <h2 id="profile-title">{{ t(confirmForget ? 'credentials.forget' : saved ? 'connections.edit' : 'connections.add') }}</h2>
      <button type="button" class="icon-button" :aria-label="t('action.close')" :disabled="busy" @click="close"><X /></button>
    </header>
    <form novalidate autocomplete="off" @submit.prevent="confirmForget ? forget() : save()">
      <div class="account-fields">
        <div v-if="confirmForget && saved" class="remove-prompt">
          <p>{{ t('account.forgetPrompt', {name:saved.name}) }}</p>
          <p class="hint">{{ t(`provider.${saved.provider}`) }} · {{ saved.endpoint || t('connections.default') }}</p>
        </div>
        <fieldset v-else :disabled="busy">
          <div class="account-pair">
            <div class="field"><label for="profile-name">{{ t('connections.name') }}</label>
              <input id="profile-name" v-model="name" :placeholder="t('account.namePlaceholder')" maxlength="120" required autofocus></div>
            <div class="field"><label for="profile-provider">{{ t('connections.provider') }}</label>
              <select id="profile-provider" v-model="provider"><option v-for="p in providers" :key="p" :value="p">{{ t(`provider.${p}`) }}</option></select></div>
          </div>
          <div id="account-guide" class="account-guide">
            <p>{{ t(`account.guide.${provider}.origin`) }}</p>
            <p class="guide-example">{{ t('account.examplePair', guide) }}</p>
            <button v-if="canApplyExample" type="button" class="inline-button fill-example" data-fill-example @click="applyExample">{{ t('account.fillExample') }}</button>
            <p v-if="capabilitiesFor(provider)?.listObjects && !capabilitiesFor(provider)?.upload" data-provider-status>{{ t('account.readOnly') }}</p>
          </div>
          <div class="field"><label for="profile-region">{{ t('connections.region') }}</label>
            <input id="profile-region" v-model="region" :placeholder="guide.region" maxlength="128" spellcheck="false" aria-describedby="account-guide"></div>
          <div class="field"><label for="profile-endpoint">{{ t('connections.endpoint') }}</label>
            <input id="profile-endpoint" v-model="endpoint" :placeholder="guide.endpoint" type="url" maxlength="2048" spellcheck="false" aria-describedby="account-guide"></div>

          <div class="keys-heading"><h3>{{ t('account.credentials') }}</h3><p class="hint">{{ t(`account.guide.${provider}.keys`) }}</p>
            <p v-if="!partial" class="hint">{{ t(newScope || clearedForScope ? 'account.newScope' : saved ? 'account.keepSecrets' : 'account.optionalSecrets') }}</p>
            <p v-if="removed" role="status" class="hint">{{ t('account.forgotten') }}</p>
          </div>
          <div class="account-pair">
            <div class="field"><label for="credential-access">{{ t('credentials.access') }}</label>
              <input id="credential-access" ref="access" @input="recordSecretPresence" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" :placeholder="t('account.accessPlaceholder')"></div>
            <div class="field"><label for="credential-secret">{{ t('credentials.secret') }}</label>
              <input id="credential-secret" ref="secret" @input="recordSecretPresence" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" :placeholder="t('account.secretPlaceholder')"></div>
          </div>
          <div class="field"><label for="credential-token">{{ t('credentials.token') }}</label>
            <input id="credential-token" ref="token" @input="recordSecretPresence" type="password" autocomplete="off" autocapitalize="none" spellcheck="false" :placeholder="t('account.tokenPlaceholder')"></div>
        </fieldset>
        <div v-if="failure || partial" ref="alertArea" class="account-errors">
          <p v-if="partial" role="status">{{ t('account.partialSave') }}</p>
          <ErrorNotice v-if="failure" :error="failure"/>
        </div>
      </div>
      <footer class="dialog-footer">
        <button v-if="saved && !confirmForget" type="button" class="inline-button remove-keys" :disabled="busy" @click="requestForget">{{ t('credentials.forget') }}</button>
        <button type="button" class="secondary" :disabled="busy" @click="confirmForget ? confirmForget = false : close()">{{ t(partial && !confirmForget ? 'action.close' : 'action.cancel') }}</button>
        <button type="submit" :class="confirmForget ? 'danger' : 'primary'" :disabled="busy || (!confirmForget && !validName)">{{ t(confirmForget ? 'credentials.confirmForget' : busy ? 'connections.saving' : 'connections.save') }}</button>
      </footer>
    </form>
  </dialog>
</template>
<style scoped>
.account-dialog { width:min(610px,94vw); overflow:hidden; }
.account-dialog[open],.account-dialog form { display:flex; flex-direction:column; }
.account-dialog form { min-height:0; }
.account-fields { overflow:auto; overscroll-behavior:contain; }
.account-fields fieldset { min-width:0; padding:18px 22px; gap:12px; }
.account-pair { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px; }
.field { display:grid; gap:5px; min-width:0; }
.field input,.field select { width:100%; min-width:0; }
.account-guide { padding:10px 13px; background:#f0f6f5; border:1px solid #d9e8e2; border-radius:6px; font-size:12px; line-height:1.6; overflow-wrap:anywhere; }
.account-guide p + p { margin-top:4px; }
.fill-example { margin:6px 0 2px; }
.guide-example { color:#315856; }
.keys-heading { border-top:1px solid var(--line); padding-top:13px; display:grid; gap:3px; }
.dialog-header,.dialog-footer { flex-shrink:0; }
.dialog-footer { align-items:center; flex-wrap:wrap; }
.remove-keys { margin-inline-end:auto; color:#9d2c30; }
.account-errors { padding:0 22px 12px; font-size:13px; line-height:1.6; }
.account-errors p + div { margin-top:8px; }
@media (max-width:520px) { .account-pair { grid-template-columns:1fr; } }
</style>
