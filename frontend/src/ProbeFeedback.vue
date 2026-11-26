<script setup lang="ts">
import { CheckCircle2, LoaderCircle, CircleAlert } from '@lucide/vue'
import { computed } from 'vue'
import { t, locale } from './i18n'
import type { Probe } from './connectionProbe'
const props = defineProps<{test:Probe}>()
const scope = computed(() => props.test.bucket
  ? t('probe.scopeBucket', {bucket:props.test.bucket})
  : t('probe.scopeAccount', {name:props.test.profile.name}))
</script>
<template>
  <div class="probe-feedback" :class="test.status" :role="test.status === 'failure' ? 'alert' : 'status'" data-probe-result>
    <LoaderCircle v-if="test.status === 'pending'" class="spin"/><CheckCircle2 v-else-if="test.status === 'success'"/><CircleAlert v-else/>
    <div v-if="test.status === 'pending'">{{ t('probe.checking', {scope}) }}</div>
    <div v-else-if="test.status === 'success'">{{ t('probe.passed', {scope, ms:test.result.latencyMs.toLocaleString(locale)}) }}</div>
    <div v-else><strong>{{ t('probe.failed', {scope}) }}</strong>
      <p>{{ t(test.error.key,test.error.detail?.params) }}</p>
      <p v-if="test.error.detail?.providerCode">{{ t('error.providerCode', {value:test.error.detail.providerCode}) }}</p>
      <p v-if="test.error.detail?.requestId">{{ t('error.requestId', {value:test.error.detail.requestId}) }}</p>
      <p v-if="!test.bucket && test.error.key === 'error.cloudAccess'">{{ t('probe.bucketAlternative') }}</p>
    </div>
  </div>
</template>
