<script setup lang="ts">
import { computed, ref } from 'vue'
import { CircleAlert, FileUp, Languages } from '@lucide/vue'
import LanguageSwitch from './LanguageSwitch.vue'
import { importLanguage, importedLanguages, locale, setLanguage, t, type MessageKey } from './i18n'

const input = ref<HTMLInputElement>()
const importing = ref(false)
const notice = ref<MessageKey>()
const activePack = computed(() => importedLanguages.value.find(item => item.locale === locale.value))

async function importFile(event: Event) {
  const selector = event.target as HTMLInputElement
  const file = selector.files?.[0]
  if (!file || importing.value) return
  importing.value = true
  try {
    await importLanguage(file)
    notice.value = 'settings.imported'
  } catch {
    notice.value = 'settings.importError'
  } finally {
    importing.value = false
    selector.value = ''
  }
}
</script>

<template>
  <section class="panel settings-panel">
    <header class="panel-head"><div class="panel-label"><Languages /><h2>{{ t('settings.appearance') }}</h2></div></header>
    <div class="setting">
      <div><h3>{{ t('settings.language') }}</h3><p>{{ t('settings.languageHint') }}</p>
        <p v-if="activePack">{{ t('settings.activePack', {name: activePack.name}) }}</p></div>
      <LanguageSwitch />
    </div>
    <details class="language-packs">
      <summary>{{ t('settings.packs') }}</summary>
      <div v-if="importedLanguages.length" class="setting">
        <label for="imported-language">{{ t('settings.packSelect') }}</label>
        <select id="imported-language" :value="activePack?.locale ?? ''" @change="setLanguage(($event.target as HTMLSelectElement).value)">
          <option value="" disabled>{{ t('settings.choosePack') }}</option>
          <option v-for="item in importedLanguages" :key="item.locale" :value="item.locale" :lang="item.locale">{{ item.name }}</option>
        </select>
      </div>
      <div class="setting import-row">
        <p>{{ t('settings.importHelp') }}</p>
        <button class="secondary" :disabled="importing" @click="input?.click()"><FileUp />{{ t('settings.import') }}</button>
        <input id="language-file" ref="input" type="file" accept=".json,application/json" hidden @change="importFile">
      </div>
      <p v-if="notice" class="import-status" role="status"><CircleAlert />{{ t(notice) }}</p>
    </details>
  </section>
</template>

<style scoped>
.language-packs { border-top: 1px solid var(--line); }
summary { padding: 14px 23px; cursor: pointer; color: var(--muted); font-size: 14px; font-weight: 600; }
summary:focus-visible { outline: 2px solid var(--accent); outline-offset: -3px; }
.language-packs .setting { padding-top: 6px; padding-bottom: 16px; flex-wrap: wrap; }
.import-row p { margin: 0; max-width: 38em; }
</style>
