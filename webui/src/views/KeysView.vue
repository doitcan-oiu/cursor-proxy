<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { CheckIcon, CopyIcon, KeyRoundIcon, LoaderCircleIcon, PlusIcon, Trash2Icon } from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { api, type ProxyKey } from '@/lib/api'
import { copyText, formatDate, relativeTime } from '@/lib/format'

const keys = ref<ProxyKey[]>([])
const loading = ref(true)

const createOpen = ref(false)
const newName = ref('')
const creating = ref(false)

// 新建后一次性展示明文
const issued = ref<{ name: string; key: string } | null>(null)
const copied = ref(false)

async function load() {
  loading.value = true
  try {
    keys.value = await api.keys.list()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    loading.value = false
  }
}

async function submitCreate() {
  creating.value = true
  try {
    const r = await api.keys.create(newName.value.trim() || 'unnamed')
    createOpen.value = false
    newName.value = ''
    issued.value = { name: r.name, key: r.key }
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    creating.value = false
  }
}

async function copyIssued() {
  if (!issued.value) return
  if (await copyText(issued.value.key)) {
    copied.value = true
    toast.success('已复制到剪贴板')
    setTimeout(() => (copied.value = false), 1600)
  }
}

async function revoke(k: ProxyKey) {
  if (!window.confirm(`吊销密钥「${k.name}」？使用它的客户端会立即失效。`)) return
  try {
    await api.keys.revoke(k.id)
    toast.success('已吊销')
    await load()
  } catch (err) {
    toast.error((err as Error).message)
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader title="访问密钥" description="签发给下游客户端的 sk- 密钥。服务端只保存哈希，明文仅在创建时显示一次。">
      <template #actions>
        <Button size="sm" @click="createOpen = true">
          <PlusIcon />
          新建密钥
        </Button>
      </template>
    </PageHeader>

    <SurfaceCard flush>
      <EmptyState
        v-if="!loading && keys.length === 0"
        :icon="KeyRoundIcon"
        title="还没有访问密钥"
        description="下游用它作为 OpenAI 的 Authorization: Bearer，或 Anthropic 的 x-api-key。"
      >
        <Button size="sm" @click="createOpen = true">
          <PlusIcon />
          新建密钥
        </Button>
      </EmptyState>

      <table v-else class="w-full text-sm">
        <thead>
          <tr class="border-b border-hairline-soft bg-surface text-left text-[12px] font-semibold text-steel">
            <th class="px-6 py-3 font-semibold">名称</th>
            <th class="px-6 py-3 font-semibold">前缀</th>
            <th class="px-6 py-3 font-semibold">创建时间</th>
            <th class="px-6 py-3 font-semibold">最近使用</th>
            <th class="px-6 py-3 text-right font-semibold">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in keys" :key="k.id" class="border-b border-hairline-soft last:border-0">
            <td class="px-6 py-3.5">
              <span class="font-medium text-ink">{{ k.name }}</span>
              <Badge v-if="k.disabled" variant="danger" class="ml-2">已停用</Badge>
            </td>
            <td class="px-6 py-3.5 font-mono text-[13px] text-steel">{{ k.prefix }}…</td>
            <td class="px-6 py-3.5 text-steel tabular">{{ formatDate(k.createdAt) }}</td>
            <td class="px-6 py-3.5 text-steel">
              {{ k.lastUsedAt ? relativeTime(k.lastUsedAt) : '从未使用' }}
            </td>
            <td class="px-6 py-3.5 text-right">
              <Button variant="destructiveGhost" size="iconSm" title="吊销" @click="revoke(k)">
                <Trash2Icon />
              </Button>
            </td>
          </tr>
        </tbody>
      </table>
    </SurfaceCard>

    <!-- 新建 -->
    <Dialog v-model:open="createOpen">
      <DialogContent class="sm:max-w-[460px]">
        <DialogHeader>
          <DialogTitle>新建访问密钥</DialogTitle>
          <DialogDescription>取个方便辨认的名字，比如使用它的应用名。</DialogDescription>
        </DialogHeader>
        <Input v-model="newName" placeholder="例如：cherry-studio" class="h-10 rounded-md" @keyup.enter="submitCreate" />
        <DialogFooter>
          <Button variant="tertiary" size="sm" @click="createOpen = false">取消</Button>
          <Button size="sm" :disabled="creating" @click="submitCreate">
            <LoaderCircleIcon v-if="creating" class="animate-spin" />
            创建
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 一次性展示明文 -->
    <Dialog :open="!!issued" @update:open="(v: boolean) => !v && (issued = null)">
      <DialogContent class="sm:max-w-[560px]">
        <DialogHeader>
          <DialogTitle>密钥已创建</DialogTitle>
          <DialogDescription>
            请立即复制保存，关闭后无法再次查看（服务端只存哈希）。
          </DialogDescription>
        </DialogHeader>
        <div class="flex items-center gap-2 rounded-md border border-hairline bg-surface px-3 py-2.5">
          <code class="min-w-0 flex-1 break-all font-mono text-[13px] text-ink">{{ issued?.key }}</code>
          <Button variant="tertiary" size="iconSm" aria-label="复制" @click="copyIssued">
            <CheckIcon v-if="copied" class="text-success-text" />
            <CopyIcon v-else />
          </Button>
        </div>
        <DialogFooter>
          <Button size="sm" @click="issued = null">我已保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
