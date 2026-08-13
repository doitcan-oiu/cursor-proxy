<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ActivityIcon, PauseIcon, PlayIcon, Trash2Icon } from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { api, type LogEntry, type LogStats } from '@/lib/api'
import { copyText, formatDuration, formatNumber, formatTime } from '@/lib/format'

const entries = ref<LogEntry[]>([])
const stats = ref<LogStats | null>(null)
const live = ref(true)
const onlyErrors = ref(false)
/** 展开了请求体的日志 id。失败时才会有请求体。 */
const expanded = ref(new Set<number>())
let timer: number | undefined

function toggle(id: number) {
  const next = new Set(expanded.value)
  next.has(id) ? next.delete(id) : next.add(id)
  expanded.value = next
}

/** 把一条失败记录整理成可直接发给别人排查的形态。 */
function report(e: LogEntry) {
  return JSON.stringify(
    {
      time: new Date(e.time).toISOString(),
      model: e.model,
      stream: e.stream,
      httpStatus: e.httpStatus,
      ms: e.ms,
      error: e.error,
      request: tryParse(e.request),
    },
    null,
    2,
  )
}

function tryParse(raw?: string) {
  if (!raw) return raw
  try {
    return JSON.parse(raw)
  } catch {
    // 超长被截断过，解析不了就原样带上
    return raw
  }
}

/** 预览时缩进展开，一整行 JSON 要横向拖着看太费劲。 */
function pretty(raw?: string) {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

async function copyRequest(e: LogEntry) {
  await copyText(report(e))
  toast.success('已复制，可直接粘贴给他人排查')
}

function exportRequest(e: LogEntry) {
  const url = URL.createObjectURL(new Blob([report(e)], { type: 'application/json' }))
  const a = document.createElement('a')
  a.href = url
  a.download = `failed-request-${e.id}.json`
  a.click()
  URL.revokeObjectURL(url)
}

const visible = computed(() => {
  const list = onlyErrors.value ? entries.value.filter((e) => e.status !== 'ok') : entries.value
  return [...list].reverse()
})

const successRate = computed(() => {
  if (!stats.value || stats.value.total === 0) return '—'
  return `${Math.round((stats.value.ok / stats.value.total) * 100)}%`
})

async function poll() {
  try {
    const r = await api.logs.get()
    entries.value = r.entries
    stats.value = r.stats
  } catch {
    /* 静默 */
  }
}

function toggleLive() {
  live.value = !live.value
  if (live.value) {
    poll()
    timer = window.setInterval(poll, 2000)
  } else {
    window.clearInterval(timer)
  }
}

async function clear() {
  try {
    await api.logs.clear()
    await poll()
    toast.success('已清空日志')
  } catch (err) {
    toast.error((err as Error).message)
  }
}

onMounted(() => {
  poll()
  timer = window.setInterval(poll, 2000)
})

onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <div>
    <PageHeader title="请求日志" description="内存环形缓冲，保留最近 500 条调用记录。">
      <template #actions>
        <Button variant="tertiary" size="sm" @click="toggleLive">
          <PauseIcon v-if="live" />
          <PlayIcon v-else />
          {{ live ? '暂停刷新' : '继续刷新' }}
        </Button>
        <Button variant="tertiary" size="sm" @click="clear">
          <Trash2Icon />
          清空
        </Button>
      </template>
    </PageHeader>

    <!-- 统计条（DESIGN.md testimonial-stat-row） -->
    <div class="mb-6 grid grid-cols-2 gap-px overflow-hidden rounded-xl border border-hairline bg-hairline-soft sm:grid-cols-4">
      <div class="bg-canvas px-6 py-5">
        <div class="text-h2 text-ink tabular">{{ formatNumber(stats?.total ?? 0) }}</div>
        <div class="mt-1 text-[13px] text-steel">总请求</div>
      </div>
      <div class="bg-canvas px-6 py-5">
        <div class="text-h2 tabular" :class="stats && stats.error > 0 ? 'text-danger' : 'text-ink'">
          {{ stats?.error ?? 0 }}
        </div>
        <div class="mt-1 text-[13px] text-steel">失败</div>
      </div>
      <div class="bg-canvas px-6 py-5">
        <div class="text-h2 text-ink tabular">{{ successRate }}</div>
        <div class="mt-1 text-[13px] text-steel">成功率</div>
      </div>
      <div class="bg-canvas px-6 py-5">
        <div class="text-h2 text-ink tabular">{{ stats?.avgMs ?? 0 }}<span class="text-lg text-steel">ms</span></div>
        <div class="mt-1 text-[13px] text-steel">平均耗时</div>
      </div>
    </div>

    <SurfaceCard flush>
      <template #actions>
        <button
          type="button"
          :class="[
            'rounded-full border px-3 py-1 text-[12px] font-medium transition-colors',
            onlyErrors ? 'border-ink bg-ink text-white' : 'border-hairline bg-canvas text-steel hover:bg-surface',
          ]"
          @click="onlyErrors = !onlyErrors"
        >
          只看失败
        </button>
      </template>

      <EmptyState
        v-if="visible.length === 0"
        :icon="ActivityIcon"
        title="暂无记录"
        :description="onlyErrors ? '当前没有失败请求。' : '下游发起调用后，这里会实时出现。'"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[860px] text-sm">
          <thead>
            <tr class="border-b border-hairline-soft bg-surface text-left text-[12px] font-semibold text-steel">
              <th class="px-6 py-3 font-semibold">时间</th>
              <th class="px-6 py-3 font-semibold">类型</th>
              <th class="px-6 py-3 font-semibold">模型</th>
              <th class="px-6 py-3 font-semibold">账号</th>
              <th class="px-6 py-3 font-semibold">密钥</th>
              <th class="px-6 py-3 text-right font-semibold">耗时</th>
              <th class="px-6 py-3 text-right font-semibold">输出</th>
              <th class="px-6 py-3 text-right font-semibold">状态</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="e in visible" :key="e.id">
              <tr class="border-b border-hairline-soft" :class="e.error ? 'border-b-0' : ''">
                <td class="px-6 py-3 text-steel tabular">{{ formatTime(e.time) }}</td>
                <td class="px-6 py-3">
                  <Badge v-if="e.stream" variant="beta">流式</Badge>
                  <Badge v-else variant="outline">{{ e.kind }}</Badge>
                </td>
                <td class="px-6 py-3 font-mono text-[13px] text-ink">{{ e.model || '—' }}</td>
                <td class="px-6 py-3 text-steel">{{ e.account || '—' }}</td>
                <td class="px-6 py-3 font-mono text-[12px] text-stone">{{ e.keyPrefix || '—' }}</td>
                <td class="px-6 py-3 text-right text-steel tabular">{{ formatDuration(e.ms) }}</td>
                <td class="px-6 py-3 text-right tabular">
                  <template v-if="e.chars">
                    <div class="text-steel">{{ e.chars }} 字</div>
                    <div v-if="e.tokens" class="text-[12px] text-stone" title="估算值">≈{{ e.tokens }} tok</div>
                  </template>
                  <span v-else class="text-steel">—</span>
                </td>
                <td class="px-6 py-3 text-right">
                  <Badge :variant="e.status === 'ok' ? 'success' : 'danger'">
                    {{ e.status === 'ok' ? '成功' : e.httpStatus || '失败' }}
                  </Badge>
                </td>
              </tr>
              <tr v-if="e.error" class="border-b border-hairline-soft">
                <td colspan="8" class="px-6 pb-3">
                  <p class="rounded-md bg-danger-bg px-3 py-2 text-[12px] leading-relaxed text-danger">
                    {{ e.error }}
                  </p>

                  <!-- 失败时留存了请求体，方便原样复现问题 -->
                  <div v-if="e.request" class="mt-2">
                    <div class="flex items-center gap-2">
                      <button
                        type="button"
                        class="rounded-full border border-hairline bg-canvas px-3 py-1 text-[12px] font-medium text-steel transition-colors hover:bg-surface"
                        @click="toggle(e.id)"
                      >
                        {{ expanded.has(e.id) ? '收起请求' : '查看请求' }}
                      </button>
                      <button
                        type="button"
                        class="rounded-full border border-hairline bg-canvas px-3 py-1 text-[12px] font-medium text-steel transition-colors hover:bg-surface"
                        @click="copyRequest(e)"
                      >
                        复制
                      </button>
                      <button
                        type="button"
                        class="rounded-full border border-hairline bg-canvas px-3 py-1 text-[12px] font-medium text-steel transition-colors hover:bg-surface"
                        @click="exportRequest(e)"
                      >
                        导出 JSON
                      </button>
                    </div>
                    <pre
                      v-if="expanded.has(e.id)"
                      class="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md bg-surface p-3 font-mono text-[12px] leading-relaxed text-charcoal"
                    >{{ pretty(e.request) }}</pre>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </SurfaceCard>
  </div>
</template>
