import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 开发时把 API 请求代理到本地 Go 服务；构建产物由 Go 用 go:embed 内嵌托管。
const GO_SERVER = process.env.GO_SERVER ?? 'http://127.0.0.1:3100'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    // 直接产出到 Go 的 embed 包目录，构建后 go build 即可把界面打进二进制。
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    chunkSizeWarningLimit: 1200,
  },
  server: {
    port: 5273,
    proxy: {
      '/manage': GO_SERVER,
      '/admin': GO_SERVER,
      '/health': GO_SERVER,
    },
  },
})
