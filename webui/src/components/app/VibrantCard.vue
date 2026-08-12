<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'

/**
 * DESIGN.md 的签名组件：32px 大圆角的品牌色实心卡。
 * 只用于「产品身份」时刻——这里用来承载概览页的核心指标。
 */
const props = withDefaults(
  defineProps<{
    tone: 'coral' | 'blue' | 'magenta' | 'purple' | 'ink'
    label: string
    value: string | number
    caption?: string
    class?: string
  }>(),
  {},
)

const toneClass = computed(
  () =>
    ({
      coral: 'bg-brand-coral',
      blue: 'bg-brand-blue',
      magenta: 'bg-brand-magenta',
      purple: 'bg-brand-purple',
      ink: 'bg-ink',
    })[props.tone],
)
</script>

<template>
  <div
    :class="
      cn(
        'relative flex min-h-[168px] flex-col justify-between overflow-hidden rounded-hero p-8 text-white',
        toneClass,
        props.class,
      )
    "
  >
    <!-- 内部径向渐变制造氛围深度，替代阴影 -->
    <div
      class="pointer-events-none absolute -right-12 -top-16 size-52 rounded-full bg-white/15 blur-2xl"
      aria-hidden="true"
    />
    <div class="relative flex items-start justify-between gap-3">
      <span class="text-[13px] font-semibold tracking-wide text-white/80">{{ label }}</span>
      <slot name="badge" />
    </div>
    <div class="relative">
      <div class="text-display tabular leading-none">{{ value }}</div>
      <p v-if="caption" class="mt-2 text-sm text-white/75">{{ caption }}</p>
    </div>
  </div>
</template>
