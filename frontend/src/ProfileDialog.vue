<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Layers3, X } from '@lucide/vue'
import { t } from './i18n'
import { workspaceError, providers, saveProfile, type Profile, type Provider, type WorkspaceError } from './workspace'

const props = defineProps<{ profile?: Profile }>()
const emit = defineEmits<{ close: [] }>()
const dialog = ref<HTMLDialogElement>()
const name = ref(props.profile?.name ?? '')
const provider = ref<Provider>(props.profile?.provider ?? 'aws')
const region = ref(props.profile?.region ?? '')
const endpoint = ref(props.profile?.endpoint ?? '')
const busy = ref(false)
const failure = ref<WorkspaceError>()
const valid = computed(() => name.value.trim().length > 0)
onMounted(() => dialog.value?.showModal())
async function save() {
  if (busy.value || !valid.value) return
  busy.value = true
  failure.value = undefined
  try {
    await saveProfile({id:props.profile?.id, name:name.value.trim(), provider:provider.value, region:region.value.trim(), endpoint:endpoint.value.trim()})
    emit('close')
  } catch (error) { failure.value = workspaceError(error) }
  finally { busy.value = false }
}
</script>

<template>
  <dialog ref="dialog" aria-labelledby="profile-title" @cancel.prevent="!busy && emit('close')">
    <header class="dialog-header">
      <span class="square-icon"><Layers3 /></span>
      <h2 id="profile-title">{{ t(profile ? 'connections.edit' : 'connections.add') }}</h2>
      <button type="button" class="icon-button" :aria-label="t('action.close')" :disabled="busy" @click="emit('close')"><X /></button>
    </header>
    <form novalidate @submit.prevent="save">
      <fieldset :disabled="busy">
        <label for="profile-name">{{ t('connections.name') }}</label><input id="profile-name" v-model="name" maxlength="120" required autofocus>
        <label for="profile-provider">{{ t('connections.provider') }}</label>
        <select id="profile-provider" v-model="provider"><option v-for="p in providers" :key="p" :value="p">{{ t(`provider.${p}`) }}</option></select>
        <label for="profile-region">{{ t('connections.region') }} <small>{{ t('connections.optional') }}</small></label>
        <input id="profile-region" v-model="region" maxlength="128" spellcheck="false">
        <label for="profile-endpoint">{{ t('connections.endpoint') }} <small v-if="provider !== 'r2'">{{ t('connections.optional') }}</small></label>
        <input id="profile-endpoint" v-model="endpoint" type="url" maxlength="2048" spellcheck="false" aria-describedby="endpoint-hint">
        <p id="endpoint-hint" class="hint">{{ t(provider === 'aws' ? 'connections.awsHint' : provider === 'r2' ? 'connections.r2Hint' : 'connections.endpointHint') }}</p>
      </fieldset>
      <p v-if="failure" role="alert" class="alert">{{ t(failure.key, failure.detail?.params) }}</p>
      <footer class="dialog-footer"><button type="button" class="secondary" :disabled="busy" @click="emit('close')">{{ t('action.cancel') }}</button>
        <button type="submit" class="primary" :disabled="busy || !valid">{{ t(busy ? 'connections.saving' : 'connections.save') }}</button></footer>
    </form>
  </dialog>
</template>
