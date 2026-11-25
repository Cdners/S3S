<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { BookOpen, ChevronDown } from '@lucide/vue'
import { t } from './i18n'
import { providerGuides } from './providerGuides'
import { providers, type Provider } from './workspace'

defineProps<{ canCreate: boolean }>()
const emit = defineEmits<{ use: [provider: Provider] }>()
const providerField = useId()
const service = ref<Provider>('aws')
const guide = computed(() => providerGuides[service.value])
</script>

<template>
  <details class="account-examples">
    <summary>
      <BookOpen aria-hidden="true" />
      <span>{{ t('account.guide.title') }}</span>
      <ChevronDown class="disclosure-arrow" aria-hidden="true" />
    </summary>
    <div class="example-body">
      <div class="example-provider">
        <label :for="providerField">{{ t('account.guide.chooseProvider') }}</label>
        <select :id="providerField" v-model="service">
          <option v-for="entry in providers" :key="entry" :value="entry">{{ t(`provider.${entry}`) }}</option>
        </select>
      </div>

      <p>{{ t(`account.guide.${service}.origin`) }}</p>
      <dl class="example-values">
        <dt>{{ t('connections.name') }}</dt>
        <dd>{{ t(`account.guide.${service}.name`) }}</dd>
        <dt>{{ t('connections.region') }}</dt>
        <dd><code>{{ guide.region }}</code></dd>
        <dt>{{ t('connections.endpoint') }}</dt>
        <dd><code>{{ guide.endpoint }}</code></dd>
      </dl>

      <div class="example-step">
        <strong>{{ t('account.guide.credentialSource') }}</strong>
        <p>{{ t(`account.guide.${service}.keys`) }}</p>
      </div>
      <div class="example-step">
        <strong>{{ t('account.guide.afterSave') }}</strong>
        <p>{{ t(`account.guide.${service}.next`) }}</p>
      </div>
      <button v-if="canCreate" class="secondary" type="button" @click="emit('use', service)">
        {{ t('account.guide.useProvider', { provider: t(`provider.${service}`) }) }}
      </button>
    </div>
  </details>
</template>

<style scoped>
.account-examples {
  margin: 0 0 16px;
  border: 1px solid var(--line);
  border-radius: 9px;
  background: white;
  font-size: 14px;
}
.account-examples summary {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 12px 16px;
  color: var(--ink);
  font-weight: 600;
  cursor: pointer;
  list-style: none;
}
.account-examples summary::-webkit-details-marker { display: none; }
.account-examples summary:focus-visible { outline: 3px solid #46b5b2; outline-offset: 2px; border-radius: 7px; }
.account-examples summary svg { width: 17px; height: 17px; color: var(--accent); flex: none; }
.disclosure-arrow { margin-left: auto; }
.account-examples[open] .disclosure-arrow { transform: rotate(180deg); }
.example-body { display: grid; gap: 13px; padding: 0 16px 16px; }
.example-provider { display: flex; flex-wrap: wrap; gap: 8px 14px; align-items: center; color: var(--ink); font-weight: 600; }
.example-body p { margin: 0; line-height: 1.55; color: var(--muted); }
.example-values {
  display: grid;
  grid-template-columns: fit-content(38%) minmax(0, 1fr);
  gap: 6px 16px;
  margin: 0;
  padding: 12px;
  border-radius: 6px;
  background: #f2f7f6;
  line-height: 1.5;
}
.example-values dt { color: var(--muted); }
.example-values dd { margin: 0; overflow-wrap: anywhere; }
.example-values code { font-size: .93em; }
.example-step strong { display: block; margin: 0 0 3px; font-weight: 600; }
.example-body button { justify-self: start; max-width: 100%; white-space: normal; }
</style>
