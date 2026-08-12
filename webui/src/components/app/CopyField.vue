<script setup lang="ts">
import { ref } from 'vue'
import { CheckIcon, CopyIcon, EyeIcon, EyeOffIcon } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { copyText } from '@/lib/format'

/** 只读的可复制字段，支持敏感值遮罩（用于 Base URL / 管理口令 / API Key）。 */
const props = withDefaults(
  defineProps<{
    label: string
    value: string
    secret?: boolean
  }>(),
  { secret: false },
)

const revealed = ref(!props.secret)
const copied = ref(false)

function masked(v: string) {
  if (revealed.value) return v
  // 短口令也必须遮罩，否则截图/共享屏幕时会直接泄露
  if (v.length <= 12) return '•'.repeat(Math.max(v.length, 8))
  return `${v.slice(0, 6)}${'•'.repeat(12)}${v.slice(-4)}`
}

async function onCopy() {
  if (await copyText(props.value)) {
    copied.value = true
    setTimeout(() => (copied.value = false), 1600)
  }
}
</script>

<template>
  <div class="flex items-center gap-3 rounded-md border border-hairline bg-surface px-3 py-2">
    <span class="w-24 shrink-0 text-[13px] font-semibold text-steel">{{ label }}</span>
    <code class="min-w-0 flex-1 truncate font-mono text-[13px] text-ink">{{ masked(value) }}</code>
    <Button
      v-if="secret"
      variant="ghost"
      size="iconXs"
      :aria-label="revealed ? '隐藏' : '显示'"
      @click="revealed = !revealed"
    >
      <EyeOffIcon v-if="revealed" />
      <EyeIcon v-else />
    </Button>
    <Button variant="ghost" size="iconXs" aria-label="复制" @click="onCopy">
      <CheckIcon v-if="copied" class="text-success-text" />
      <CopyIcon v-else />
    </Button>
  </div>
</template>
