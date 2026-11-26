<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Gauge } from '@lucide/vue'
import ErrorNotice from './ErrorNotice.vue'
import { t } from './i18n'
import { call, workspaceError, type WorkspaceError } from './workspace'

const modes = ['full', 'office', 'custom'] as const
type Mode = typeof modes[number]
interface Options {
  mode: Mode
  uploadBytesPerSecond: number
  downloadBytesPerSecond: number
}

const saved = ref<Options>()
const mode = ref<Mode>('full')
const upload = ref<string | number>(1024)
const download = ref<string | number>(4096)
const busy = ref(false)
const applied = ref(false)
const error = ref<WorkspaceError>()
const kibibyte = 1024
const maxKiB = 2 ** 30

function bytes(text: string | number) {
  const value = Number(text) * kibibyte
  return String(text).trim() !== '' && Number.isSafeInteger(value) && value >= 0 && value <= maxKiB * kibibyte ? value : NaN
}
const draft = computed<Options>(() => ({
  mode: mode.value,
  uploadBytesPerSecond: mode.value === 'custom' ? bytes(upload.value) : mode.value === 'office' ? 2 ** 20 : 0,
  downloadBytesPerSecond: mode.value === 'custom' ? bytes(download.value) : mode.value === 'office' ? 4 * 2 ** 20 : 0
}))
const valid = computed(() => Number.isFinite(draft.value.uploadBytesPerSecond) && Number.isFinite(draft.value.downloadBytesPerSecond))
const changed = computed(() => saved.value?.mode !== draft.value.mode ||
  saved.value.uploadBytesPerSecond !== draft.value.uploadBytesPerSecond ||
  saved.value.downloadBytesPerSecond !== draft.value.downloadBytesPerSecond)

async function sync(save: boolean) {
  if (busy.value || (save && (!saved.value || !valid.value))) return
  busy.value = true
  error.value = undefined
  applied.value = false
  try {
    const options = save
      ? await call<Options>('SaveTransferOptions', draft.value)
      : await call<Options>('GetTransferOptions')
    saved.value = options
    mode.value = options.mode
    if (options.mode === 'custom') {
      upload.value = String(options.uploadBytesPerSecond / kibibyte)
      download.value = String(options.downloadBytesPerSecond / kibibyte)
    }
    applied.value = save
  } catch (cause) {
    error.value = workspaceError(cause)
  } finally {
    busy.value = false
  }
}
onMounted(() => sync(false))
</script>

<template>
  <section class="panel bandwidth" aria-labelledby="bandwidth-heading">
    <header class="panel-head"><div class="panel-label"><Gauge aria-hidden="true" /><h2 id="bandwidth-heading">{{ t('bandwidth.title') }}</h2></div></header>
    <div class="controls">
      <ErrorNotice v-if="error" :error="error" />
      <template v-if="saved">
        <p class="hint">{{ t('bandwidth.hint') }}</p>
        <form @submit.prevent="sync(true)" @input="applied=false" @change="applied=false">
          <fieldset :disabled="busy">
            <legend class="sr-only">{{ t('bandwidth.title') }}</legend>
            <label for="speed-mode">{{ t('bandwidth.mode') }}</label>
            <select id="speed-mode" v-model="mode"><option v-for="choice in modes" :key="choice" :value="choice">{{ t(`bandwidth.${choice}`) }}</option></select>
            <p v-if="mode === 'office'" class="hint">{{ t('bandwidth.officeHint') }}</p>
            <div v-if="mode === 'custom'" class="limits">
              <div><label for="upload-limit">{{ t('bandwidth.upload') }}</label>
                <input id="upload-limit" v-model="upload" required type="number" min="0" :max="maxKiB" step="any" aria-describedby="units-hint"></div>
              <div><label for="download-limit">{{ t('bandwidth.download') }}</label>
                <input id="download-limit" v-model="download" required type="number" min="0" :max="maxKiB" step="any" aria-describedby="units-hint"></div>
              <p id="units-hint" class="hint">{{ t('bandwidth.zero') }}</p>
            </div>
          </fieldset>
          <div class="apply-row"><button class="primary" :disabled="busy || !valid || !changed" type="submit">{{ t('bandwidth.apply') }}</button>
            <span role="status">{{ applied && !changed ? t('bandwidth.saved') : '' }}</span></div>
        </form>
      </template>
      <p v-else-if="busy" role="status" class="hint">{{ t('common.loading') }}</p>
      <button v-else type="button" class="secondary" @click="sync(false)">{{ t('bandwidth.reload') }}</button>
    </div>
  </section>
</template>

<style scoped>
.controls { padding: 16px 22px 20px; }
.hint { margin: 0; color: var(--muted); font-size: 14px; }
fieldset { padding: 0; gap: 6px; margin: 14px 0; }
select { width: min(100%, 18rem); }
.limits { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 10rem), 1fr)); gap: 10px 16px; max-width: 32rem; margin-top: 8px; }
.limits div { display: grid; gap: 6px; }
.limits input { width: 100%; min-width: 0; }
.limits p { grid-column: 1 / -1; }
.apply-row { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; font-size: 14px; color: var(--muted); }
</style>
