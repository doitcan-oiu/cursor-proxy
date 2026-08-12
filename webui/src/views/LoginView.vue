<script setup lang="ts">
import { ref } from 'vue'
import { ArrowRightIcon, LoaderCircleIcon } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useAuth } from '@/composables/useAuth'

/**
 * 登录门：只需要 ADMIN_TOKEN。
 * 版式沿用 DESIGN.md 的 hero-band——超大字号标题 + 副标题 + 药丸 CTA。
 */
const { login } = useAuth()
const token = ref('')
const loading = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await login(token.value)
  } catch (err) {
    error.value = (err as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="flex min-h-screen flex-col bg-canvas">
    <div class="flex flex-1 items-center justify-center px-5 py-16">
      <div class="w-full max-w-[520px]">
        <div class="mb-10 text-center">
          <span
            class="mx-auto mb-6 flex size-12 items-center justify-center rounded-xl bg-ink text-lg font-bold text-white"
          >
            C
          </span>
          <h1 class="text-h1 text-ink sm:text-[56px] sm:leading-[1.1] sm:tracking-[-1.5px]">Cursor Proxy</h1>
          <p class="mt-3 text-subtitle text-steel">
            把 Cursor 订阅暴露成 OpenAI / Anthropic 兼容 API
          </p>
        </div>

        <form class="rounded-xl border border-hairline bg-canvas p-6" @submit.prevent="submit">
          <label for="admin-token" class="mb-2 block text-[13px] font-semibold text-charcoal">
            管理口令 ADMIN_TOKEN
          </label>
          <Input
            id="admin-token"
            v-model="token"
            type="password"
            autocomplete="current-password"
            placeholder="admin_xxxxxxxx"
            class="h-10 rounded-md font-mono"
            :aria-invalid="!!error"
          />
          <p v-if="error" class="mt-2 text-[13px] text-danger">{{ error }}</p>
          <p v-else class="mt-2 text-[13px] text-stone">
            启动服务时终端会打印该口令，也可用环境变量 ADMIN_TOKEN 固定。
          </p>

          <Button type="submit" class="mt-5 w-full" :disabled="loading">
            <LoaderCircleIcon v-if="loading" class="animate-spin" />
            <template v-else>进入控制台<ArrowRightIcon /></template>
          </Button>
        </form>
      </div>
    </div>

    <footer class="border-t border-hairline-soft px-5 py-5 text-center text-micro text-faded">
      口令只保存在本机浏览器，不会上传到任何第三方。
    </footer>
  </div>
</template>
