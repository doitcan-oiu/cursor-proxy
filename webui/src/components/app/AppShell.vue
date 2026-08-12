<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import {
  ActivityIcon,
  BoxesIcon,
  GaugeIcon,
  GlobeIcon,
  KeyRoundIcon,
  LogOutIcon,
  MenuIcon,
  MessagesSquareIcon,
  PuzzleIcon,
  UsersRoundIcon,
  XIcon,
} from '@lucide/vue'
import { Button } from '@/components/ui/button'
import StatusDot from '@/components/app/StatusDot.vue'
import { useAuth } from '@/composables/useAuth'
import { api } from '@/lib/api'

const { info, logout } = useAuth()
const route = useRoute()
const drawerOpen = ref(false)

// 未识别工具数量做成角标：出问题时能第一时间被看到
const unknownCount = ref(0)
let timer: number | undefined

async function pollUnknown() {
  try {
    unknownCount.value = (await api.unknownTools.list()).length
  } catch {
    /* 静默 */
  }
}

onMounted(() => {
  pollUnknown()
  timer = window.setInterval(pollUnknown, 15000)
})
onUnmounted(() => window.clearInterval(timer))

const nav = [
  { to: '/', label: '概览', icon: GaugeIcon },
  { to: '/accounts', label: '账号池', icon: UsersRoundIcon },
  { to: '/keys', label: '访问密钥', icon: KeyRoundIcon },
  { to: '/models', label: '模型', icon: BoxesIcon },
  { to: '/playground', label: '调试台', icon: MessagesSquareIcon },
  { to: '/network', label: '出口网络', icon: GlobeIcon },
  { to: '/logs', label: '请求日志', icon: ActivityIcon },
  { to: '/unknown-tools', label: '未识别工具', icon: PuzzleIcon, badge: () => unknownCount.value },
]

function isActive(to: string) {
  return to === '/' ? route.path === '/' : route.path.startsWith(to)
}
</script>

<template>
  <div class="min-h-screen bg-canvas">
    <!-- 顶部黑色状态条（DESIGN.md promo-banner 位置），这里承载服务运行状态 -->
    <div
      class="sticky top-0 z-40 flex h-9 items-center justify-center gap-2 bg-ink px-5 text-[13px] font-medium text-white"
    >
      <StatusDot tone="ok" pulse />
      <span>服务运行中</span>
      <span class="text-white/40">·</span>
      <code class="font-mono text-white/80">{{ info?.baseUrl }}</code>
    </div>

    <div class="mx-auto flex w-full max-w-[1440px]">
      <!-- 左侧固定导航（≥1024px） -->
      <aside
        class="sticky top-9 hidden h-[calc(100vh-2.25rem)] w-[232px] shrink-0 flex-col border-r border-hairline-soft px-4 py-6 lg:flex"
      >
        <RouterLink to="/" class="mb-8 flex items-center gap-2.5 px-2">
          <span class="flex size-7 items-center justify-center rounded-md bg-ink text-[13px] font-bold text-white">
            C
          </span>
          <span class="text-[15px] font-semibold tracking-tight text-ink">Cursor Proxy</span>
        </RouterLink>

        <nav class="flex flex-col gap-0.5">
          <RouterLink
            v-for="item in nav"
            :key="item.to"
            :to="item.to"
            :class="[
              'flex items-center gap-2.5 rounded-sm px-3 py-2 text-sm transition-colors',
              isActive(item.to)
                ? 'bg-surface font-medium text-ink'
                : 'text-charcoal hover:bg-surface/70 hover:text-ink',
            ]"
          >
            <component :is="item.icon" class="size-4 shrink-0" />
            <span class="flex-1">{{ item.label }}</span>
            <span
              v-if="item.badge && item.badge() > 0"
              class="rounded-full bg-brand-coral px-1.5 py-0.5 text-[11px] font-semibold leading-none text-white tabular"
            >
              {{ item.badge() }}
            </span>
          </RouterLink>
        </nav>

        <div class="mt-auto px-1">
          <Button variant="tertiary" size="sm" class="w-full" @click="logout">
            <LogOutIcon />
            退出管理
          </Button>
        </div>
      </aside>

      <!-- 移动端顶栏 + 抽屉 -->
      <div class="min-w-0 flex-1">
        <div class="flex items-center justify-between border-b border-hairline-soft px-5 py-3 lg:hidden">
          <div class="flex items-center gap-2">
            <span class="flex size-7 items-center justify-center rounded-md bg-ink text-[13px] font-bold text-white">
              C
            </span>
            <span class="text-[15px] font-semibold text-ink">Cursor Proxy</span>
          </div>
          <Button variant="tertiary" size="iconSm" aria-label="菜单" @click="drawerOpen = true">
            <MenuIcon />
          </Button>
        </div>

        <main class="px-5 py-8 sm:px-8 lg:px-10 lg:py-10">
          <RouterView v-slot="{ Component }">
            <component :is="Component" />
          </RouterView>
        </main>
      </div>
    </div>

    <!-- 移动抽屉 -->
    <Transition
      enter-active-class="transition-opacity duration-150"
      leave-active-class="transition-opacity duration-150"
      enter-from-class="opacity-0"
      leave-to-class="opacity-0"
    >
      <div v-if="drawerOpen" class="fixed inset-0 z-50 lg:hidden">
        <div class="absolute inset-0 bg-ink/40" @click="drawerOpen = false" />
        <div class="absolute inset-y-0 left-0 flex w-72 flex-col bg-canvas px-4 py-5 shadow-e4">
          <div class="mb-6 flex items-center justify-between">
            <span class="text-[15px] font-semibold text-ink">导航</span>
            <Button variant="ghost" size="iconSm" aria-label="关闭" @click="drawerOpen = false">
              <XIcon />
            </Button>
          </div>
          <nav class="flex flex-col gap-1">
            <RouterLink
              v-for="item in nav"
              :key="item.to"
              :to="item.to"
              class="flex items-center gap-3 rounded-sm px-3 py-3 text-sm text-charcoal"
              :class="isActive(item.to) ? 'bg-surface font-medium text-ink' : ''"
              @click="drawerOpen = false"
            >
              <component :is="item.icon" class="size-4" />
              {{ item.label }}
            </RouterLink>
          </nav>
          <Button variant="tertiary" size="sm" class="mt-auto" @click="logout">
            <LogOutIcon />
            退出管理
          </Button>
        </div>
      </div>
    </Transition>
  </div>
</template>
