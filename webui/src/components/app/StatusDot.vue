<script setup lang="ts">
import { computed } from 'vue'

/** 小圆点状态指示，配合文字使用。 */
const props = withDefaults(
  defineProps<{
    tone: 'ok' | 'warn' | 'error' | 'idle'
    pulse?: boolean
  }>(),
  { pulse: false },
)

const toneClass = computed(
  () =>
    ({
      ok: 'bg-success-text',
      warn: 'bg-brand-coral',
      error: 'bg-danger',
      idle: 'bg-stone',
    })[props.tone],
)
</script>

<template>
  <span class="relative inline-flex size-2 shrink-0">
    <span
      v-if="pulse"
      :class="['absolute inline-flex size-full animate-ping rounded-full opacity-60', toneClass]"
      aria-hidden="true"
    />
    <span :class="['relative inline-flex size-2 rounded-full', toneClass]" />
  </span>
</template>
