<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { CheckIcon, CopyIcon, DownloadIcon, PuzzleIcon, RefreshCwIcon, Trash2Icon } from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { api, type UnknownTool } from '@/lib/api'
import { copyText, formatTime } from '@/lib/format'

/**
 * 上游随时可能新增内置工具。没映射的工具会让那一轮任务做不了，
 * 这里把它们的字段号与参数结构留档，导出后即可据此补上映射。
 */
const items = ref<UnknownTool[]>([])
const loading = ref(true)
const copied = ref(false)
let timer: number | undefined

async function load() {
  try {
    items.value = await api.unknownTools.list()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    loading.value = false
  }
}

function exportJSON() {
  const payload = JSON.stringify(
    { exportedAt: new Date().toISOString(), unknownTools: items.value },
    null,
    2,
  )
  const blob = new Blob([payload], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `unknown-tools-${Date.now()}.json`
  a.click()
  URL.revokeObjectURL(url)
  toast.success('已导出')
}

async function copyAll() {
  const payload = JSON.stringify(items.value, null, 2)
  if (await copyText(payload)) {
    copied.value = true
    toast.success('已复制到剪贴板')
    setTimeout(() => (copied.value = false), 1600)
  }
}

async function clear() {
  if (!window.confirm('清空全部未识别工具记录？')) return
  try {
    await api.unknownTools.clear()
    await load()
    toast.success('已清空')
  } catch (err) {
    toast.error((err as Error).message)
  }
}

onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <div>
    <PageHeader
      title="未识别工具"
      description="上游下发了本代理还不认识的内置工具时会记录在这里。导出后可据此补上映射。"
    >
      <template #actions>
        <Button variant="ghost" size="sm" :disabled="loading" @click="load">
          <RefreshCwIcon :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
        <Button variant="tertiary" size="sm" :disabled="items.length === 0" @click="copyAll">
          <CheckIcon v-if="copied" class="text-success-text" />
          <CopyIcon v-else />
          复制
        </Button>
        <Button variant="tertiary" size="sm" :disabled="items.length === 0" @click="clear">
          <Trash2Icon />
          清空
        </Button>
        <Button size="sm" :disabled="items.length === 0" @click="exportJSON">
          <DownloadIcon />
          导出 JSON
        </Button>
      </template>
    </PageHeader>

    <SurfaceCard v-if="!loading && items.length === 0">
      <EmptyState
        :icon="PuzzleIcon"
        title="没有未识别的工具"
        description="说明上游用到的内置工具目前都已支持。若某次任务只回一句就停下，回来看看这里。"
      />
    </SurfaceCard>

    <div v-else class="flex flex-col gap-4">
      <SurfaceCard v-for="it in items" :key="it.field">
        <template #actions>
          <Badge variant="warn">出现 {{ it.count }} 次</Badge>
        </template>

              <div class="flex flex-wrap items-center gap-2">
                <Badge v-if="it.name" variant="code">{{ it.name }}</Badge>
                <Badge variant="code">字段 {{ it.field }}</Badge>
          <span class="text-[13px] text-steel">{{ it.model }}</span>
          <span class="text-[13px] text-stone tabular">{{ formatTime(it.time) }}</span>
        </div>

        <p v-if="it.hint" class="mt-3 text-sm text-ink">
          <span class="text-steel">线索：</span>{{ it.hint }}
        </p>

        <div v-if="it.structure" class="mt-3">
          <div class="mb-1.5 text-[12px] font-semibold text-steel">参数结构</div>
          <pre class="overflow-x-auto rounded-md bg-surface p-3 font-mono text-[12px] leading-relaxed text-charcoal">{{ it.structure }}</pre>
        </div>
      </SurfaceCard>
    </div>
  </div>
</template>
