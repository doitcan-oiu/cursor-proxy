<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  CheckCheckIcon,
  DownloadIcon,
  LoaderCircleIcon,
  PlusIcon,
  RefreshCwIcon,
  RouteIcon,
  Trash2Icon,
  UsersRoundIcon,
} from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import StatusDot from '@/components/app/StatusDot.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type Account, type AccountCheck, type AccountHealth } from '@/lib/api'
import { expiryState, formatExpiry } from '@/lib/format'

const accounts = ref<Account[]>([])
const health = ref<AccountHealth[]>([])
const checks = ref<Record<string, AccountCheck>>({})
const loading = ref(true)
const checkingAll = ref(false)
const busyId = ref('')

const importOpen = ref(false)
const importText = ref('')
const importing = ref(false)

const addOpen = ref(false)
const addToken = ref('')
const addLabel = ref('')
const adding = ref(false)

const proxyOpen = ref(false)
const proxyTarget = ref<Account | null>(null)
const proxyUrl = ref('')

const healthOf = computed(() => {
  const map: Record<string, AccountHealth> = {}
  for (const h of health.value) map[h.id] = h
  return map
})

async function load() {
  loading.value = true
  try {
    const [a, h] = await Promise.all([api.accounts.list(), api.accounts.health()])
    accounts.value = a
    health.value = h
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    loading.value = false
  }
}

async function checkOne(id: string) {
  busyId.value = id
  try {
    checks.value = { ...checks.value, [id]: await api.accounts.check(id) }
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    busyId.value = ''
  }
}

async function checkAll() {
  checkingAll.value = true
  try {
    const results = await api.accounts.checkAll()
    const next: Record<string, AccountCheck> = {}
    for (const r of results) next[r.id] = r
    checks.value = next
    const ok = results.filter((r) => r.ok).length
    toast.success(`验号完成：${ok} 个可用 / 共 ${results.length} 个`)
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    checkingAll.value = false
  }
}

async function remove(acc: Account) {
  if (!window.confirm(`确定删除账号「${acc.label}」？`)) return
  busyId.value = acc.id
  try {
    await api.accounts.remove(acc.id)
    toast.success('已删除')
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    busyId.value = ''
  }
}

async function submitImport() {
  if (!importText.value.trim()) return
  importing.value = true
  try {
    const r = await api.accounts.import(importText.value)
    toast.success(`导入完成：新增 ${r.added}，重复 ${r.duplicates}，无效 ${r.invalid}`)
    importOpen.value = false
    importText.value = ''
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    importing.value = false
  }
}

async function submitAdd() {
  if (!addToken.value.trim()) return
  adding.value = true
  try {
    const r = await api.accounts.add(addToken.value, addLabel.value)
    toast.success(r.exchanged ? '已添加（web token 已自动换取）' : '已添加')
    addOpen.value = false
    addToken.value = ''
    addLabel.value = ''
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    adding.value = false
  }
}

function openProxy(acc: Account) {
  proxyTarget.value = acc
  proxyUrl.value = acc.proxyUrl ?? ''
  proxyOpen.value = true
}

async function submitProxy() {
  if (!proxyTarget.value) return
  try {
    await api.accounts.setProxy(proxyTarget.value.id, proxyUrl.value)
    toast.success(proxyUrl.value.trim() ? '已设置独立出口' : '已清除独立出口')
    proxyOpen.value = false
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader title="账号池" description="导入 Cursor 凭证，调度器会按健康度与并发自动轮换。">
      <template #actions>
        <Button variant="tertiary" size="sm" :disabled="checkingAll || accounts.length === 0" @click="checkAll">
          <LoaderCircleIcon v-if="checkingAll" class="animate-spin" />
          <CheckCheckIcon v-else />
          全部验号
        </Button>
        <Button variant="secondary" size="sm" @click="importOpen = true">
          <DownloadIcon />
          批量导入
        </Button>
        <Button size="sm" @click="addOpen = true">
          <PlusIcon />
          添加账号
        </Button>
      </template>
    </PageHeader>

    <SurfaceCard flush>
      <template #actions>
        <Button variant="ghost" size="xs" :disabled="loading" @click="load">
          <RefreshCwIcon :class="loading ? 'animate-spin' : ''" />
        </Button>
      </template>

      <EmptyState
        v-if="!loading && accounts.length === 0"
        :icon="UsersRoundIcon"
        title="还没有账号"
        description="粘贴浏览器 Cookie 里的 WorkosCursorSessionToken，或桌面端提取的 accessToken。web token 会自动换成可对话的 session token。"
      >
        <Button size="sm" @click="importOpen = true">
          <DownloadIcon />
          批量导入
        </Button>
      </EmptyState>

      <!--
        表格用 table-fixed + 固定列宽：内容再长也只会被截断，不会把列撑变形。
        次要列（机器码、出口）在窄屏隐藏，信息改为收进主列的副标题。
      -->
      <div v-else class="overflow-x-auto">
        <table class="w-full min-w-[720px] table-fixed text-sm">
          <colgroup>
            <col class="w-[30%]" />
            <col class="w-[15%]" />
            <col class="w-[15%]" />
            <col class="hidden w-[14%] xl:table-column" />
            <col class="w-[26%]" />
            <col class="w-[116px]" />
          </colgroup>
          <thead>
            <tr class="border-b border-hairline-soft bg-surface text-left text-[12px] font-semibold text-steel">
              <th class="px-6 py-3 font-semibold">账号</th>
              <th class="px-4 py-3 font-semibold">到期</th>
              <th class="px-4 py-3 font-semibold">调度</th>
              <th class="hidden px-4 py-3 font-semibold xl:table-cell">独立出口</th>
              <th class="px-4 py-3 font-semibold">验号结果</th>
              <th class="px-6 py-3 text-right font-semibold">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="acc in accounts" :key="acc.id" class="border-b border-hairline-soft last:border-0 align-middle">
              <td class="px-6 py-3.5">
                <div class="flex items-center gap-2.5">
                  <StatusDot
                    :tone="
                      healthOf[acc.id]?.quarantinedForMs
                        ? 'error'
                        : healthOf[acc.id]?.cooldownForMs
                          ? 'warn'
                          : healthOf[acc.id]?.available
                            ? 'ok'
                            : 'idle'
                    "
                  />
                  <div class="min-w-0">
                    <div class="truncate font-medium text-ink" :title="acc.label">{{ acc.label }}</div>
                    <div class="truncate font-mono text-[12px] text-stone" :title="`${acc.preview} · 机器码 ${acc.machineCode}`">
                      {{ acc.preview }}
                      <span class="text-faded">· {{ acc.machineCode }}</span>
                    </div>
                  </div>
                </div>
              </td>

              <td class="px-4 py-3.5">
                <div
                  class="tabular truncate text-[13px]"
                  :class="
                    expiryState(acc.expiresAt) === 'expired'
                      ? 'text-danger'
                      : expiryState(acc.expiresAt) === 'soon'
                        ? 'text-warn-text'
                        : 'text-steel'
                  "
                >
                  {{ formatExpiry(acc.expiresAt) }}
                </div>
                <Badge v-if="acc.hasRefresh" variant="beta" class="mt-1">自动续期</Badge>
              </td>

              <td class="px-4 py-3.5">
                <Badge v-if="healthOf[acc.id]?.quarantinedForMs" variant="danger">隔离中</Badge>
                <Badge v-else-if="healthOf[acc.id]?.cooldownForMs" variant="warn">冷却中</Badge>
                <Badge v-else-if="healthOf[acc.id]?.available" variant="success">可用</Badge>
                <Badge v-else variant="neutral">—</Badge>
                <div v-if="healthOf[acc.id]?.inFlight" class="mt-1 text-[12px] text-steel tabular">
                  {{ healthOf[acc.id].inFlight }} 在途
                </div>
              </td>

              <td class="hidden px-4 py-3.5 xl:table-cell">
                <div
                  v-if="acc.proxyUrl"
                  class="truncate font-mono text-[12px] text-brand-blue-700"
                  :title="acc.proxyUrl"
                >
                  {{ acc.proxyUrl }}
                </div>
                <span v-else class="text-[13px] text-stone">跟随全局</span>
              </td>

              <td class="px-4 py-3.5">
                <template v-if="checks[acc.id]">
                  <div class="flex flex-wrap items-center gap-1.5">
                    <Badge :variant="checks[acc.id].ok ? 'success' : 'danger'">
                      {{ checks[acc.id].ok ? '正常' : '失败' }}
                    </Badge>
                    <Badge v-if="checks[acc.id].plan" variant="outline">{{ checks[acc.id].plan }}</Badge>
                    <Badge v-if="checks[acc.id].exhausted" variant="warn">额度用尽</Badge>
                  </div>
                  <p class="mt-1 truncate text-[12px] text-stone" :title="checks[acc.id].detail">
                    {{ checks[acc.id].detail }}
                  </p>
                </template>
                <span v-else class="text-[13px] text-stone">未验</span>
              </td>

              <td class="px-6 py-3.5">
                <div class="flex items-center justify-end gap-0.5">
                  <Button
                    variant="ghost"
                    size="iconSm"
                    title="验号"
                    :disabled="busyId === acc.id"
                    @click="checkOne(acc.id)"
                  >
                    <LoaderCircleIcon v-if="busyId === acc.id" class="animate-spin" />
                    <CheckCheckIcon v-else />
                  </Button>
                  <Button variant="ghost" size="iconSm" title="设置独立出口" @click="openProxy(acc)">
                    <RouteIcon />
                  </Button>
                  <Button variant="destructiveGhost" size="iconSm" title="删除" @click="remove(acc)">
                    <Trash2Icon />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </SurfaceCard>

    <!-- 批量导入 -->
    <Dialog v-model:open="importOpen">
      <DialogContent class="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>批量导入凭证</DialogTitle>
          <DialogDescription>
            每行一条。可用 <code class="font-mono">token,标签</code> 附带备注，<code class="font-mono">#</code>
            开头的行会被忽略。web token 会自动换取为可对话的 session token。
          </DialogDescription>
        </DialogHeader>
        <Textarea
          v-model="importText"
          rows="9"
          spellcheck="false"
          placeholder="user_xxx%3A%3AeyJhbGciOi...,主号&#10;eyJhbGciOi...,备用号"
          class="resize-y rounded-md font-mono text-[13px]"
        />
        <DialogFooter>
          <Button variant="tertiary" size="sm" @click="importOpen = false">取消</Button>
          <Button size="sm" :disabled="importing || !importText.trim()" @click="submitImport">
            <LoaderCircleIcon v-if="importing" class="animate-spin" />
            开始导入
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 添加单个 -->
    <Dialog v-model:open="addOpen">
      <DialogContent class="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle>添加账号</DialogTitle>
          <DialogDescription>粘贴单条 Cursor 凭证。</DialogDescription>
        </DialogHeader>
        <div class="space-y-3">
          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">凭证 Token</label>
            <Textarea
              v-model="addToken"
              rows="4"
              spellcheck="false"
              placeholder="user_xxx%3A%3AeyJhbGciOi..."
              class="resize-y rounded-md font-mono text-[13px]"
            />
          </div>
          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">标签（可选）</label>
            <Input v-model="addLabel" placeholder="例如：主号" class="h-10 rounded-md" />
          </div>
        </div>
        <DialogFooter>
          <Button variant="tertiary" size="sm" @click="addOpen = false">取消</Button>
          <Button size="sm" :disabled="adding || !addToken.trim()" @click="submitAdd">
            <LoaderCircleIcon v-if="adding" class="animate-spin" />
            添加
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 独立出口 -->
    <Dialog v-model:open="proxyOpen">
      <DialogContent class="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle>独立出口代理</DialogTitle>
          <DialogDescription>
            为「{{ proxyTarget?.label }}」单独指定上游出口，留空则跟随全局设置。
          </DialogDescription>
        </DialogHeader>
        <Input v-model="proxyUrl" placeholder="http://127.0.0.1:7890" class="h-10 rounded-md font-mono" />
        <DialogFooter>
          <Button variant="tertiary" size="sm" @click="proxyOpen = false">取消</Button>
          <Button size="sm" @click="submitProxy">保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
