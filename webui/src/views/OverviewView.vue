<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { ArrowUpRightIcon, RefreshCwIcon } from '@lucide/vue'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import VibrantCard from '@/components/app/VibrantCard.vue'
import CopyField from '@/components/app/CopyField.vue'
import StatusDot from '@/components/app/StatusDot.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { api, type AccountHealth, type LogEntry, type LogStats, type VpnStatus } from '@/lib/api'
import { formatDuration, formatNumber, formatTime } from '@/lib/format'
import { useAuth } from '@/composables/useAuth'

const { info } = useAuth()

const health = ref<AccountHealth[]>([])
const stats = ref<LogStats | null>(null)
const recent = ref<LogEntry[]>([])
const vpn = ref<VpnStatus | null>(null)
const modelCount = ref<number | null>(null)
const loading = ref(true)

let timer: number | undefined

const availableCount = computed(() => health.value.filter((h) => h.available).length)
const inFlightTotal = computed(() => health.value.reduce((s, h) => s + h.inFlight, 0))
const successRate = computed(() => {
  if (!stats.value || stats.value.total === 0) return '—'
  return `${Math.round((stats.value.ok / stats.value.total) * 100)}%`
})

async function refresh() {
  try {
    const [h, logs, v] = await Promise.all([api.accounts.health(), api.logs.get(), api.vpn.status()])
    health.value = h
    stats.value = logs.stats
    recent.value = [...logs.entries].reverse().slice(0, 8)
    vpn.value = v
  } catch {
    /* 轮询失败静默，避免打断查看 */
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  await refresh()
  // 模型列表要打上游，单独拉且不参与轮询
  api.models
    .list()
    .then((m) => (modelCount.value = m.length))
    .catch(() => (modelCount.value = 0))
  timer = window.setInterval(refresh, 5000)
})

onUnmounted(() => window.clearInterval(timer))
</script>

<template>
  <div>
    <PageHeader title="概览" description="账号池、流量与出口状态的实时快照。">
      <template #actions>
        <Button variant="tertiary" size="sm" :disabled="loading" @click="refresh">
          <RefreshCwIcon :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <!-- 签名产品卡矩阵：核心指标 -->
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <VibrantCard
        tone="coral"
        label="可用账号"
        :value="`${availableCount}/${health.length}`"
        :caption="inFlightTotal > 0 ? `${inFlightTotal} 个请求在途` : '当前空闲'"
      />
      <VibrantCard
        tone="blue"
        label="近一分钟请求"
        :value="stats?.lastMinute ?? 0"
        :caption="`累计 ${formatNumber(stats?.total ?? 0)} 次调用`"
      />
      <VibrantCard tone="purple" label="成功率" :value="successRate" :caption="`平均耗时 ${stats?.avgMs ?? 0}ms`" />
      <VibrantCard
        tone="magenta"
        label="可用模型"
        :value="modelCount ?? '—'"
        :caption="vpn?.running ? `出口：${vpn.current || '机场节点'}` : '出口：直连'"
      />
    </div>

    <div class="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-3">
      <!-- 接入信息 -->
      <SurfaceCard title="接入信息" description="下游客户端按这里配置即可" class="lg:col-span-1">
        <div class="flex flex-col gap-2">
          <CopyField label="Base URL" :value="info?.baseUrl ?? ''" />
          <CopyField label="管理口令" :value="info?.adminToken ?? ''" secret />
        </div>
        <div class="mt-5 space-y-2.5">
          <p class="text-[13px] font-semibold text-charcoal">已开放端点</p>
          <div class="flex flex-wrap gap-1.5">
            <Badge variant="code">/v1/chat/completions</Badge>
            <Badge variant="code">/v1/messages</Badge>
            <Badge variant="code">/v1/models</Badge>
          </div>
        </div>
        <RouterLink to="/keys">
          <Button variant="secondary" size="sm" class="mt-5 w-full">
            管理访问密钥
            <ArrowUpRightIcon />
          </Button>
        </RouterLink>
      </SurfaceCard>

      <!-- 账号健康 -->
      <SurfaceCard title="账号健康" description="调度器实时状态" class="lg:col-span-2">
        <template #actions>
          <RouterLink to="/accounts">
            <Button variant="ghost" size="xs">全部账号<ArrowUpRightIcon /></Button>
          </RouterLink>
        </template>
        <div v-if="health.length === 0" class="py-10 text-center text-[13px] text-stone">
          还没有账号。先到「账号池」导入 Cursor 凭证。
        </div>
        <ul v-else class="divide-y divide-hairline-soft">
          <li v-for="h in health.slice(0, 6)" :key="h.id" class="flex items-center gap-3 py-3 first:pt-0">
            <StatusDot :tone="h.available ? 'ok' : h.quarantinedForMs > 0 ? 'error' : 'warn'" />
            <span class="min-w-0 flex-1 truncate text-sm text-ink">{{ h.label }}</span>
            <span v-if="h.inFlight > 0" class="text-[12px] text-steel tabular">{{ h.inFlight }} 在途</span>
            <Badge v-if="h.quarantinedForMs > 0" variant="danger">隔离中</Badge>
            <Badge v-else-if="h.cooldownForMs > 0" variant="warn">冷却中</Badge>
            <Badge v-else-if="h.available" variant="success">可用</Badge>
            <Badge v-else variant="neutral">满载</Badge>
          </li>
        </ul>
      </SurfaceCard>
    </div>

    <!-- 近期请求 -->
    <SurfaceCard title="近期请求" class="mt-6" flush>
      <template #actions>
        <RouterLink to="/logs">
          <Button variant="ghost" size="xs">完整日志<ArrowUpRightIcon /></Button>
        </RouterLink>
      </template>
      <div v-if="recent.length === 0" class="py-10 text-center text-[13px] text-stone">暂无请求记录。</div>
      <table v-else class="w-full text-sm">
        <thead>
          <tr class="border-b border-hairline-soft bg-surface text-left text-[12px] font-semibold text-steel">
            <th class="px-6 py-2.5 font-semibold">时间</th>
            <th class="px-6 py-2.5 font-semibold">模型</th>
            <th class="px-6 py-2.5 font-semibold">账号</th>
            <th class="px-6 py-2.5 font-semibold">耗时</th>
            <th class="px-6 py-2.5 text-right font-semibold">状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in recent" :key="e.id" class="border-b border-hairline-soft last:border-0">
            <td class="px-6 py-3 text-steel tabular">{{ formatTime(e.time) }}</td>
            <td class="px-6 py-3 text-ink">{{ e.model || '—' }}</td>
            <td class="px-6 py-3 text-steel">{{ e.account || '—' }}</td>
            <td class="px-6 py-3 text-steel tabular">{{ formatDuration(e.ms) }}</td>
            <td class="px-6 py-3 text-right">
              <Badge :variant="e.status === 'ok' ? 'success' : 'danger'">
                {{ e.status === 'ok' ? '成功' : '失败' }}
              </Badge>
            </td>
          </tr>
        </tbody>
      </table>
    </SurfaceCard>
  </div>
</template>
