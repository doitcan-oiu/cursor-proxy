<script setup lang="ts">
/** 页面标题区：40px 标题 + 说明文字，右侧放页面级操作。 */
defineProps<{
  title: string
  description?: string
}>()
</script>

<template>
  <header class="flex flex-wrap items-end justify-between gap-4 pb-6">
    <div class="min-w-0">
      <h1 class="text-h1 text-ink">{{ title }}</h1>
      <p v-if="description" class="mt-2 max-w-2xl text-[15px] text-steel">{{ description }}</p>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <slot name="actions" />
    </div>
  </header>
</template>
