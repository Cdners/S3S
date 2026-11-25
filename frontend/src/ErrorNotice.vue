<script setup lang="ts">
import { CircleAlert } from '@lucide/vue'
import { t } from './i18n'
import type { WorkspaceError } from './workspace'

defineProps<{ error: WorkspaceError }>()
</script>

<template>
  <div class="notice cloud-error" role="alert">
    <CircleAlert aria-hidden="true"/>
    <div><p>{{ t(error.key, error.detail?.params) }}</p>
      <p v-if="error.detail?.providerCode">{{ t('error.providerCode', {value:error.detail.providerCode}) }}</p>
      <p v-if="error.detail?.requestId">{{ t('error.requestId', {value:error.detail.requestId}) }}</p>
    </div>
  </div>
</template>
