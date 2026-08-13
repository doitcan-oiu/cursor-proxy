<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { BrainIcon, ImagePlusIcon, PlayIcon, SquareIcon, Trash2Icon, XIcon } from '@lucide/vue'
import { toast } from 'vue-sonner'
import PageHeader from '@/components/app/PageHeader.vue'
import SurfaceCard from '@/components/app/SurfaceCard.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { api, streamTestChat, type Account, type ModelInfo } from '@/lib/api'

/** 调试台：直连账号发起一次对话，验证凭证与模型是否真的可用。 */
const models = ref<ModelInfo[]>([])
const accounts = ref<Account[]>([])
const model = ref('auto')
const accountId = ref('__auto__')
const prompt = ref('用一句话介绍你自己。')

const content = ref('')
const reasoning = ref('')
const error = ref('')
const running = ref(false)
const elapsed = ref(0)

/** 已附带的图片，以 data URL 保存，发送时原样交给后端解码。 */
type Attachment = { name: string; url: string; size: number }
const images = ref<Attachment[]>([])
const dragging = ref(false)
const fileInput = ref<HTMLInputElement | null>(null)

// 与后端 types.MaxImageBytes 对齐，超了先在本地拦下
const MAX_IMAGE_BYTES = 20 * 1024 * 1024

let handle: { abort: () => void } | null = null
let ticker: number | undefined

function readAsDataURL(file: File) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(new Error('读取失败'))
    reader.readAsDataURL(file)
  })
}

async function addFiles(files: FileList | File[] | null) {
  if (!files) return
  for (const file of Array.from(files)) {
    if (!file.type.startsWith('image/')) {
      toast.error(`${file.name} 不是图片`)
      continue
    }
    if (file.size > MAX_IMAGE_BYTES) {
      toast.error(`${file.name} 超过 20MB`)
      continue
    }
    try {
      images.value.push({ name: file.name || '粘贴的图片', url: await readAsDataURL(file), size: file.size })
    } catch {
      toast.error(`${file.name} 读取失败`)
    }
  }
}

function onPaste(e: ClipboardEvent) {
  const files = Array.from(e.clipboardData?.items ?? [])
    .filter((i) => i.kind === 'file')
    .map((i) => i.getAsFile())
    .filter((f): f is File => f !== null)
  if (files.length > 0) {
    e.preventDefault()
    void addFiles(files)
  }
}

function onDrop(e: DragEvent) {
  dragging.value = false
  void addFiles(e.dataTransfer?.files ?? null)
}

function formatSize(bytes: number) {
  return bytes < 1024 * 1024 ? `${Math.round(bytes / 1024)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function run() {
  if ((!prompt.value.trim() && images.value.length === 0) || running.value) return
  content.value = ''
  reasoning.value = ''
  error.value = ''
  elapsed.value = 0
  running.value = true

  const startedAt = Date.now()
  ticker = window.setInterval(() => (elapsed.value = Date.now() - startedAt), 100)

  const finish = () => {
    running.value = false
    window.clearInterval(ticker)
    elapsed.value = Date.now() - startedAt
    handle = null
  }

  handle = streamTestChat(
    {
      model: model.value,
      prompt: prompt.value,
      accountId: accountId.value === '__auto__' ? '' : accountId.value,
      images: images.value.map((i) => i.url),
    },
    {
      onDelta: (d) => {
        if (d.content) content.value += d.content
        if (d.reasoning) reasoning.value += d.reasoning
      },
      onError: (m) => {
        error.value = m
        finish()
      },
      onDone: finish,
    },
  )
}

function stop() {
  handle?.abort()
  handle = null
  running.value = false
  window.clearInterval(ticker)
}

function clear() {
  content.value = ''
  reasoning.value = ''
  error.value = ''
  elapsed.value = 0
}

onMounted(async () => {
  try {
    const [m, a] = await Promise.all([api.models.list(), api.accounts.list()])
    models.value = m
    accounts.value = a
    if (m.length > 0 && !m.some((x) => x.id === 'auto' || x.aliases.includes('auto'))) {
      model.value = m[0].id
    }
  } catch (err) {
    toast.error((err as Error).message)
  }
})

onUnmounted(() => {
  handle?.abort()
  window.clearInterval(ticker)
})
</script>

<template>
  <div>
    <PageHeader title="调试台" description="不消耗访问密钥，直接用池中账号跑一次真实对话，快速验证可用性。" />

    <div class="grid grid-cols-1 gap-6 lg:grid-cols-5">
      <!-- 输入 -->
      <SurfaceCard title="请求" class="lg:col-span-2">
        <div class="space-y-4">
          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">模型</label>
            <Select v-model="model">
              <SelectTrigger class="h-10 w-full rounded-md">
                <SelectValue placeholder="选择模型" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">auto（自动选择）</SelectItem>
                <SelectItem v-for="m in models" :key="m.id" :value="m.id">{{ m.id }}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">账号</label>
            <Select v-model="accountId">
              <SelectTrigger class="h-10 w-full rounded-md">
                <SelectValue placeholder="自动选择" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__auto__">自动选择（池中第一个）</SelectItem>
                <SelectItem v-for="a in accounts" :key="a.id" :value="a.id">{{ a.label }}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div>
            <label class="mb-1.5 block text-[13px] font-semibold text-charcoal">提示词</label>
            <Textarea
              v-model="prompt"
              rows="7"
              class="resize-y rounded-md text-[13px]"
              @paste="onPaste"
            />
          </div>

          <!-- 图片：验证多模态。支持点选、粘贴、拖入 -->
          <div>
            <div class="mb-1.5 flex items-center justify-between">
              <label class="text-[13px] font-semibold text-charcoal">图片</label>
              <Button variant="ghost" size="sm" @click="fileInput?.click()">
                <ImagePlusIcon />
                添加
              </Button>
            </div>

            <input
              ref="fileInput"
              type="file"
              accept="image/*"
              multiple
              class="hidden"
              @change="addFiles(($event.target as HTMLInputElement).files); ($event.target as HTMLInputElement).value = ''"
            />

            <div
              class="rounded-md border border-dashed px-3 py-3 transition-colors"
              :class="dragging ? 'border-accent bg-accent-bg' : 'border-hairline'"
              @dragover.prevent="dragging = true"
              @dragleave.prevent="dragging = false"
              @drop.prevent="onDrop"
            >
              <div v-if="images.length === 0" class="text-center text-[12px] text-stone">
                拖入图片，或在提示词里直接粘贴截图
              </div>

              <div v-else class="flex flex-wrap gap-2">
                <div
                  v-for="(img, i) in images"
                  :key="i"
                  class="group relative overflow-hidden rounded-md border border-hairline"
                >
                  <img :src="img.url" :alt="img.name" class="size-16 object-cover" />
                  <button
                    class="absolute right-0.5 top-0.5 rounded bg-ink/70 p-0.5 text-white opacity-0 transition-opacity group-hover:opacity-100"
                    :title="`移除 ${img.name}`"
                    @click="images.splice(i, 1)"
                  >
                    <XIcon class="size-3" />
                  </button>
                  <span
                    class="absolute inset-x-0 bottom-0 bg-ink/60 px-1 text-center text-[10px] text-white tabular"
                  >{{ formatSize(img.size) }}</span>
                </div>
              </div>
            </div>
          </div>

          <div class="flex items-center gap-2">
            <Button v-if="!running" class="flex-1" :disabled="!prompt.trim() && images.length === 0" @click="run">
              <PlayIcon />
              发送
            </Button>
            <Button v-else variant="secondary" class="flex-1" @click="stop">
              <SquareIcon />
              停止
            </Button>
            <Button variant="tertiary" size="icon" title="清空输出" @click="clear">
              <Trash2Icon />
            </Button>
          </div>
        </div>
      </SurfaceCard>

      <!-- 输出 -->
      <SurfaceCard title="响应" class="lg:col-span-3">
        <template #actions>
          <Badge v-if="running" variant="beta">生成中</Badge>
          <Badge v-else-if="elapsed > 0 && !error" variant="success">{{ (elapsed / 1000).toFixed(1) }}s</Badge>
          <Badge v-if="content" variant="outline">{{ content.length }} 字</Badge>
        </template>

        <div v-if="error" class="mb-4 rounded-md border border-danger/30 bg-danger-bg px-4 py-3 text-[13px] text-danger">
          {{ error }}
        </div>

        <div v-if="reasoning" class="mb-4 rounded-lg border border-hairline bg-surface p-4">
          <div class="mb-2 flex items-center gap-1.5 text-[12px] font-semibold text-steel">
            <BrainIcon class="size-3.5" />
            思考过程
          </div>
          <pre class="whitespace-pre-wrap break-words font-sans text-[13px] leading-relaxed text-slate">{{ reasoning }}</pre>
        </div>

        <pre
          v-if="content"
          class="whitespace-pre-wrap break-words font-sans text-sm leading-relaxed text-charcoal"
        >{{ content }}</pre>

        <div
          v-else-if="!running && !error"
          class="flex min-h-[220px] items-center justify-center text-[13px] text-stone"
        >
          发送后这里会实时显示生成内容。
        </div>

        <div v-else-if="running && !content" class="flex min-h-[220px] items-center justify-center gap-2 text-[13px] text-stone">
          <span class="size-1.5 animate-bounce rounded-full bg-stone [animation-delay:-0.3s]" />
          <span class="size-1.5 animate-bounce rounded-full bg-stone [animation-delay:-0.15s]" />
          <span class="size-1.5 animate-bounce rounded-full bg-stone" />
        </div>
      </SurfaceCard>
    </div>
  </div>
</template>
