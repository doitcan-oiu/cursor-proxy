<script setup lang="ts">
import { cn } from '@/lib/utils'

/**
 * 安静的白底细线卡（DESIGN.md card-base，16px 圆角），与鲜艳卡形成半径反差。
 *
 * 内容区默认带 24px 内边距；表格类内容传 flush 取消内边距，让表头/行自己贴边。
 * 注意：这里用 flush 而不是 padded，因为 Vue 会把未传的布尔 prop 转成 false，
 * 「默认为真」的布尔 prop 会永远失效。
 */
const props = defineProps<{
  title?: string
  description?: string
  flush?: boolean
  class?: string
}>()
</script>

<template>
  <section :class="cn('rounded-xl border border-hairline bg-canvas', props.class)">
    <header
      v-if="title || $slots.actions"
      class="flex items-center justify-between gap-3 border-b border-hairline-soft px-6 py-4"
    >
      <div class="min-w-0">
        <h2 v-if="title" class="text-card text-ink">{{ title }}</h2>
        <p v-if="description" class="mt-0.5 text-[13px] text-steel">{{ description }}</p>
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <slot name="actions" />
      </div>
    </header>
    <div :class="flush ? '' : 'p-6'">
      <slot />
    </div>
  </section>
</template>
