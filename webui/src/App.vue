<script setup lang="ts">
import { onMounted } from 'vue'
import { LoaderCircleIcon } from '@lucide/vue'
import AppShell from '@/components/app/AppShell.vue'
import LoginView from '@/views/LoginView.vue'
import { Toaster } from '@/components/ui/sonner'
import { useAuth } from '@/composables/useAuth'

const { authed, ready, restore } = useAuth()

onMounted(restore)
</script>

<template>
  <div v-if="!ready" class="flex min-h-screen items-center justify-center bg-canvas">
    <LoaderCircleIcon class="size-5 animate-spin text-stone" />
  </div>
  <AppShell v-else-if="authed" />
  <LoginView v-else />

  <Toaster position="top-center" :duration="3200" rich-colors close-button />
</template>
