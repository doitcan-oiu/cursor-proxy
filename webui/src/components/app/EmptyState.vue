<script setup lang="ts">
import type { Component } from 'vue'

/** 空态：图标 + 一句说明 + 可选操作。 */
defineProps<{
  icon?: Component
  title: string
  description?: string
}>()
</script>

<template>
  <div class="flex flex-col items-center justify-center gap-3 px-6 py-14 text-center">
    <div v-if="icon" class="flex size-11 items-center justify-center rounded-full bg-surface text-stone">
      <component :is="icon" class="size-5" />
    </div>
    <div>
      <p class="text-[15px] font-semibold text-ink">{{ title }}</p>
      <p v-if="description" class="mt-1 max-w-md text-[13px] text-steel">{{ description }}</p>
    </div>
    <div class="mt-1">
      <slot />
    </div>
  </div>
</template>
