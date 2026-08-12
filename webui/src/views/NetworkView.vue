<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  DownloadCloudIcon,
  GaugeIcon,
  GlobeIcon,
  LoaderCircleIcon,
  PowerIcon,
  PowerOffIcon,
  SaveIcon,
} from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import StatusDot from '@/components/app/StatusDot.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, type VpnMode, type VpnStatus } from '@/lib/api'

/** 出口网络：内置 Mihomo 机场的订阅、策略、节点切换。 */
const status = ref<VpnStatus | null>(null)
const subUrl = ref('')
const mode = ref<VpnMode>('url-test')
const busy = ref('')

const modes: { value: VpnMode; label: string; hint: string }[] = [
  { value: 'url-test', label: '自动测速', hint: '选延迟最低的节点，失效自动切换' },
  { value: 'fallback', label: '故障转移', hint: '按顺序用第一个可用节点' },
  { value: 'load-balance', label: '负载均衡', hint: '轮询分摊到不同节点' },
]

const sortedNodes = computed(() => {
  const nodes = status.value?.nodes ?? []
  return [...nodes].sort((a, b) => {
    if (a.delay === 0) return 1
    if (b.delay === 0) return -1
    return a.delay - b.delay
  })
})

function delayTone(delay: number) {
  if (delay === 0) return 'neutral' as const
  if (delay < 200) return 'success' as const
  if (delay < 500) return 'warn' as const
  return 'danger' as const
}

async function load() {
  try {
    const s = await api.vpn.status()
    status.value = s
    subUrl.value = s.subUrl
    mode.value = s.mode
  } catch (err) {
    toast.error((err as Error).message)
  }
}

async function withBusy(key: string, fn: () => Promise<void>) {
  busy.value = key
  try {
    await fn()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    busy.value = ''
  }
}

const saveSub = () =>
  withBusy('save', async () => {
    await api.vpn.setSub(subUrl.value)
    await api.vpn.setMode(mode.value)
    toast.success('已保存')
    await load()
  })

const enable = () =>
  withBusy('enable', async () => {
    if (!subUrl.value.trim()) {
      toast.error('请先填写机场订阅地址')
      return
    }
    await api.vpn.enable(subUrl.value, mode.value)
    toast.success('已启用，上游流量走机场节点')
    await load()
  })

const disable = () =>
  withBusy('disable', async () => {
    await api.vpn.disable()
    toast.success('已停用，恢复直连')
    await load()
  })

const install = () =>
  withBusy('install', async () => {
    const r = await api.vpn.install()
    toast[r.installed ? 'success' : 'error'](r.installed ? '内核已就绪' : '内核安装失败')
    await load()
  })

const testDelays = () =>
  withBusy('test', async () => {
    status.value = await api.vpn.test()
    toast.success('测速完成')
  })

const switchNode = (name: string) =>
  withBusy(name, async () => {
    await api.vpn.switch(name)
    toast.success(`已切换到 ${name}`)
    await load()
  })

onMounted(load)
</script>

<template>
  <div>
    <PageHeader title="出口网络" description="内置 Mihomo 内核。启用后所有上游请求走机场节点，可绕开模型的区域限制。" />

    <div class="grid grid-cols-1 gap-6 lg:grid-cols-5">
      <!-- 配置 -->
      <SurfaceCard title="机场配置" class="lg:col-span-3">
        <template #actions>
          <Badge v-if="status?.running" variant="success">
            <StatusDot tone="ok" pulse />
            运行中
          </Badge>
          <Badge v-else-if="status?.installed" variant="neutral">已停用</Badge>
          <Badge v-else variant="warn">未安装内核</Badge>
        </template>

        <div class="space-y-5">
          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">订阅地址</label>
            <Input
              v-model="subUrl"
              placeholder="https://your-airport.com/api/v1/client/subscribe?token=..."
              class="h-10 rounded-md font-mono text-[13px]"
            />
          </div>

          <div>
            <label class="mb-2 block text-[13px] font-semibold text-charcoal">节点策略</label>
            <div class="flex flex-wrap gap-2">
              <button
                v-for="m in modes"
                :key="m.value"
                type="button"
                :class="[
                  'rounded-full border px-4 py-1.5 text-[13px] font-medium transition-colors',
                  mode === m.value
                    ? 'border-ink bg-ink text-white'
                    : 'border-hairline bg-canvas text-steel hover:bg-surface',
                ]"
                @click="mode = m.value"
              >
                {{ m.label }}
              </button>
            </div>
            <p class="mt-2 text-[12px] text-stone">{{ modes.find((m) => m.value === mode)?.hint }}</p>
          </div>

          <div class="flex flex-wrap items-center gap-2 border-t border-hairline-soft pt-5">
            <Button v-if="!status?.running" :disabled="busy === 'enable'" @click="enable">
              <LoaderCircleIcon v-if="busy === 'enable'" class="animate-spin" />
              <PowerIcon v-else />
              启用
            </Button>
            <Button v-else variant="secondary" :disabled="busy === 'disable'" @click="disable">
              <LoaderCircleIcon v-if="busy === 'disable'" class="animate-spin" />
              <PowerOffIcon v-else />
              停用
            </Button>

            <Button variant="tertiary" :disabled="busy === 'save'" @click="saveSub">
              <SaveIcon />
              保存配置
            </Button>

            <Button v-if="!status?.installed" variant="tertiary" :disabled="busy === 'install'" @click="install">
              <LoaderCircleIcon v-if="busy === 'install'" class="animate-spin" />
              <DownloadCloudIcon v-else />
              下载内核
            </Button>

            <Button
              variant="ghost"
              :disabled="!status?.running || busy === 'test'"
              class="ml-auto"
              @click="testDelays"
            >
              <LoaderCircleIcon v-if="busy === 'test'" class="animate-spin" />
              <GaugeIcon v-else />
              测速
            </Button>
          </div>

          <div v-if="status?.running" class="rounded-md border border-hairline bg-surface px-3 py-2 text-[13px]">
            <span class="text-steel">本地出口：</span>
            <code class="font-mono text-ink">{{ status.proxyUrl }}</code>
          </div>
        </div>
      </SurfaceCard>

      <!-- 节点 -->
      <SurfaceCard
        title="节点"
        :description="status?.current ? `当前：${status.current}` : undefined"
        class="lg:col-span-2"
        flush
      >
        <EmptyState
          v-if="!status?.running"
          :icon="GlobeIcon"
          title="未启用机场"
          description="填写订阅地址并启用后，这里会列出全部节点与延迟。"
        />
        <div v-else-if="sortedNodes.length === 0" class="py-12 text-center text-[13px] text-stone">
          暂无节点，试试点「测速」刷新。
        </div>
        <ul v-else class="max-h-[520px] divide-y divide-hairline-soft overflow-y-auto">
          <li
            v-for="n in sortedNodes"
            :key="n.name"
            class="flex items-center gap-3 px-6 py-2.5"
            :class="n.name === status?.current ? 'bg-surface' : ''"
          >
            <StatusDot :tone="n.name === status?.current ? 'ok' : 'idle'" />
            <span class="min-w-0 flex-1 truncate text-[13px] text-ink" :title="n.name">{{ n.name }}</span>
            <Badge :variant="delayTone(n.delay)">{{ n.delay > 0 ? `${n.delay}ms` : '超时' }}</Badge>
            <Button
              v-if="n.name !== status?.current"
              variant="ghost"
              size="xs"
              :disabled="busy === n.name"
              @click="switchNode(n.name)"
            >
              切换
            </Button>
          </li>
        </ul>
      </SurfaceCard>
    </div>
  </div>
</template>
