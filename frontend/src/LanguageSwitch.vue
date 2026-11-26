<script setup lang="ts">
import { useId } from 'vue'
import { builtinLanguages, locale, setLanguage, t } from './i18n'

defineProps<{ compact?: boolean }>()
const group = useId()
</script>

<template>
  <fieldset class="language-switch" :class="{compact}">
    <legend class="sr-only">{{ t('settings.language') }}</legend>
    <label v-for="language in builtinLanguages" :key="language.locale" class="language-choice">
      <input type="radio" :name="group" :value="language.locale" :checked="locale === language.locale"
        @change="setLanguage(language.locale)">
      <span :lang="language.locale">{{ language.name }}</span>
    </label>
  </fieldset>
</template>

<style scoped>
.language-switch {
  --surface: #f1f4f5;
  --edge: #c7d3d5;
  --text: #3d535a;
  --selected: #fff;
  --active-text: #005e59;
  display: inline-flex;
  align-items: stretch;
  margin: 0;
  padding: 3px;
  border: 1px solid var(--edge);
  border-radius: 8px;
  background: var(--surface);
}
.language-choice {
  position: relative;
  display: flex;
  flex: 1 1 0;
  min-width: 0;
  font-size: 14px;
  font-weight: 550;
}
.language-choice input {
  position: absolute;
  inset: 0;
  margin: 0;
  width: 100%;
  height: 100%;
  min-height: 0;
  opacity: 0;
  cursor: pointer;
}
.language-choice span {
  flex: 1;
  padding: 6px 12px;
  color: var(--text);
  border: 1px solid transparent;
  border-radius: 5px;
  white-space: nowrap;
  text-align: center;
}
input:checked + span {
  background: var(--selected);
  border-color: var(--edge);
  color: var(--active-text);
  font-weight: 700;
}
input:focus-visible + span { outline: 2px solid #37aba9; outline-offset: 1px; }
.compact {
  --surface: #1b3037;
  --edge: #4a656a;
  --text: #d3e1e3;
  --selected: #345149;
  --active-text: #d7faed;
  margin: 11px 0 0;
  width: 100%;
  box-sizing: border-box;
  padding: 2px;
}
.compact .language-choice { font-size: 12px; }
.compact .language-choice span { padding: 5px 4px; }
@media (max-width:860px) { .compact { width:130px; margin:0; } }
@media (forced-colors: active) {
  input:checked + span { border-color: Highlight; }
}
</style>
