<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'

const readPercent = ref<string | number>('')
const writePercent = ref<string | number>('')
const loading = ref(true)
const saving = ref(false)
const loadError = ref('')

const draft = computed(() => ({
  readPercent: readPercent.value === '' ? NaN : Number(readPercent.value),
  writePercent: writePercent.value === '' ? NaN : Number(writePercent.value),
}))
const validationMessage = computed(() => {
  const { readPercent: read, writePercent: write } = draft.value
  if ([read, write].some((value) => !Number.isInteger(value) || value < 0 || value > 100)) {
    return '请输入 0 到 100 之间的整数。'
  }
  return read + write > 100 ? '读写比例之和不能超过 100%。' : ''
})

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    const settings = await api.simulatedCache.get()
    readPercent.value = settings.readPercent
    writePercent.value = settings.writePercent
  } catch (err) {
    loadError.value = (err as Error).message
  } finally {
    loading.value = false
  }
}

async function save() {
  if (loading.value || saving.value || loadError.value || validationMessage.value) return
  saving.value = true
  try {
    const settings = await api.simulatedCache.save(draft.value)
    readPercent.value = settings.readPercent
    writePercent.value = settings.writePercent
    toast.success('模拟缓存设置已保存')
  } catch (err) {
    toast.error((err as Error).message)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <SurfaceCard title="模拟缓存" description="按输入 token 的比例模拟缓存读写，保存后对新请求生效。">
    <p v-if="loading" role="status" class="text-[13px] text-steel">正在读取模拟缓存设置…</p>
    <div v-else-if="loadError" class="flex flex-wrap items-center gap-3">
      <p role="alert" class="text-[13px] text-danger">{{ loadError }}</p>
      <Button variant="tertiary" size="sm" @click="load">重试</Button>
    </div>
    <form v-else class="space-y-3" @submit.prevent="save">
      <div class="flex flex-wrap items-end gap-4">
        <div class="w-36 space-y-1.5">
          <label for="cache-read-percent" class="block text-[13px] font-semibold text-charcoal">
            缓存读取比例 (%)
          </label>
          <Input
            id="cache-read-percent"
            v-model="readPercent"
            type="number"
            min="0"
            max="100"
            step="1"
            required
            aria-describedby="cache-percent-hint"
            :disabled="saving"
          />
        </div>
        <div class="w-36 space-y-1.5">
          <label for="cache-write-percent" class="block text-[13px] font-semibold text-charcoal">
            缓存写入比例 (%)
          </label>
          <Input
            id="cache-write-percent"
            v-model="writePercent"
            type="number"
            min="0"
            max="100"
            step="1"
            required
            aria-describedby="cache-percent-hint"
            :disabled="saving"
          />
        </div>
        <Button type="submit" size="sm" :disabled="saving || !!validationMessage">
          {{ saving ? '正在保存…' : '保存缓存设置' }}
        </Button>
      </div>
      <p id="cache-percent-hint" aria-live="polite" class="text-[13px]" :class="validationMessage ? 'text-danger' : 'text-steel'">
        {{ validationMessage || '填 0 可关闭对应项，读写合计最多 100%。' }}
      </p>
    </form>
  </SurfaceCard>
</template>
