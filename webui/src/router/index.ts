import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

const routes: RouteRecordRaw[] = [
  { path: '/', name: 'overview', component: () => import('@/views/OverviewView.vue'), meta: { title: '概览' } },
  { path: '/accounts', name: 'accounts', component: () => import('@/views/AccountsView.vue'), meta: { title: '账号池' } },
  { path: '/keys', name: 'keys', component: () => import('@/views/KeysView.vue'), meta: { title: '访问密钥' } },
  { path: '/models', name: 'models', component: () => import('@/views/ModelsView.vue'), meta: { title: '模型' } },
  { path: '/playground', name: 'playground', component: () => import('@/views/PlaygroundView.vue'), meta: { title: '调试台' } },
  { path: '/network', name: 'network', component: () => import('@/views/NetworkView.vue'), meta: { title: '出口网络' } },
  { path: '/logs', name: 'logs', component: () => import('@/views/LogsView.vue'), meta: { title: '请求日志' } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

router.afterEach((to) => {
  const title = (to.meta.title as string) ?? ''
  document.title = title ? `${title} · Cursor Proxy` : 'Cursor Proxy'
})
