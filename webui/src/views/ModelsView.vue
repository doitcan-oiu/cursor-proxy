<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { BoxesIcon, CheckIcon, CopyIcon, RefreshCwIcon, SearchIcon } from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import EmptyState from '@/components/app/EmptyState.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, type ModelInfo } from '@/lib/api'
import { copyText } from '@/lib/format'

const models = ref<ModelInfo[]>([])
const loading = ref(true)
const query = ref('')
const copiedId = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter(
    (m) => m.id.toLowerCase().includes(q) || m.aliases.some((a) => a.toLowerCase().includes(q)),
  )
})

async function load() {
  loading.value = true
  try {
    models.value = await api.models.list()
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    loading.value = false
  }
}

async function copyId(id: string) {
  if (await copyText(id)) {
    copiedId.value = id
    setTimeout(() => (copiedId.value = ''), 1500)
  }
}

onMounted(load)
</script>

<template>
  <div>
    <PageHeader title="模型" description="用池中任一可用账号从上游拉取。这里的名称可直接作为请求里的 model 传入。">
      <template #actions>
        <Button variant="tertiary" size="sm" :disabled="loading" @click="load">
          <RefreshCwIcon :class="loading ? 'animate-spin' : ''" />
          刷新
        </Button>
      </template>
    </PageHeader>

    <div class="mb-5 flex items-center gap-2 rounded-md border border-hairline bg-surface px-3 h-9 max-w-sm">
      <SearchIcon class="size-4 shrink-0 text-stone" />
      <Input
        v-model="query"
        placeholder="搜索模型名或别名"
        class="h-auto border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0"
      />
    </div>

    <SurfaceCard flush>
      <EmptyState
        v-if="!loading && models.length === 0"
        :icon="BoxesIcon"
        title="拉不到模型列表"
        description="需要池中至少有一个可用账号。请先到「账号池」导入并验号。"
      />
      <div v-else-if="filtered.length === 0" class="py-12 text-center text-[13px] text-stone">
        没有匹配「{{ query }}」的模型。
      </div>
      <ul v-else class="divide-y divide-hairline-soft">
        <li v-for="m in filtered" :key="m.id" class="flex items-center gap-4 px-6 py-3.5">
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <span class="truncate font-mono text-[13px] font-medium text-ink">{{ m.id }}</span>
              <Badge v-if="m.aliases.includes('auto')" variant="new">默认</Badge>
            </div>
            <div v-if="m.displayName" class="mt-0.5 truncate text-[13px] text-steel">{{ m.displayName }}</div>
          </div>
          <div class="hidden flex-wrap justify-end gap-1.5 sm:flex">
            <Badge v-for="a in m.aliases" :key="a" variant="outline">{{ a }}</Badge>
          </div>
          <Button variant="ghost" size="iconSm" title="复制模型名" @click="copyId(m.id)">
            <CheckIcon v-if="copiedId === m.id" class="text-success-text" />
            <CopyIcon v-else />
          </Button>
        </li>
      </ul>
    </SurfaceCard>
  </div>
</template>
