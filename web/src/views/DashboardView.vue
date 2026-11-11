<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { auth, type PlatformConfig, type Workspace, type WorkspaceImage } from '../store'
import ConnectionCard from '../components/ConnectionCard.vue'
import CreateWorkspaceModal from '../components/CreateWorkspaceModal.vue'

const workspaces = ref<Workspace[]>([])
const config = ref<PlatformConfig | null>(null)
const images = ref<WorkspaceImage[]>([])
const error = ref('')
const busy = ref('')
const showCreate = ref(false)
const expanded = ref<string | null>(null)

let timer: number | undefined

async function refresh() {
  try {
    workspaces.value = await api.get('/api/workspaces')
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      await auth.load()
    } else {
      error.value = (e as Error).message
    }
  }
}

onMounted(async () => {
  config.value = await api.get('/api/config')
  try {
    images.value = await api.get('/api/workspace-images')
  } catch {
    images.value = [] // picker falls back to the platform default
  }
  await refresh()
  timer = window.setInterval(refresh, 4000)
})
onUnmounted(() => window.clearInterval(timer))

async function action(ws: Workspace, act: 'start' | 'stop' | 'delete') {
  if (act === 'delete') {
    if (!confirm(`Delete workspace "${ws.name}" including ALL files on its volume?`)) return
    busy.value = ws.name + ':' + act
    try {
      await api.del(`/api/workspaces/${ws.name}`)
    } catch (e) {
      error.value = (e as Error).message
    }
  } else {
    busy.value = ws.name + ':' + act
    try {
      await api.post(`/api/workspaces/${ws.name}/${act}`)
    } catch (e) {
      error.value = (e as Error).message
    }
  }
  busy.value = ''
  await refresh()
}

function phaseClass(phase: string) {
  return {
    Running: 'badge-running',
    Provisioning: 'badge-provisioning',
    Stopped: 'badge-stopped',
    Failed: 'badge-failed',
  }[phase] ?? 'badge-stopped'
}
</script>

<template>
  <div>
    <div class="spread">
      <h1>Workspaces</h1>
      <button class="btn btn-primary" @click="showCreate = true">+ New workspace</button>
    </div>

    <div class="error-banner" v-if="error">{{ error }}</div>

    <div class="empty" v-if="!workspaces.length">
      <p>No workspaces yet.</p>
      <p class="muted">
        A workspace is a pod in your own namespace with a persistent home
        directory. Create one, then connect with VS Code Remote-SSH or
        JetBrains Gateway.
      </p>
    </div>

    <div class="grid">
      <div class="card" v-for="ws in workspaces" :key="ws.name">
        <div class="spread" style="margin-bottom: 0.5rem">
          <div class="row">
            <router-link :to="`/workspace/${ws.name}`" style="font-size: 1.05rem; font-weight: 700">{{
              ws.name
            }}</router-link>
            <span class="badge" :class="phaseClass(ws.phase)">{{ ws.phase }}</span>
          </div>
          <span class="muted">{{ ws.class }}</span>
        </div>
        <p class="muted" style="margin: 0 0 0.75rem" v-if="ws.message">{{ ws.message }}</p>
        <p class="mono muted" style="margin: 0 0 0.75rem" :title="ws.image">
          {{ ws.gitRepos?.length ? ws.gitRepos.join(', ') : ws.image.split('/').pop() }}
        </p>
        <div class="row" style="flex-wrap: wrap">
          <button
            v-if="ws.phase === 'Stopped' || ws.phase === 'Failed'"
            class="btn btn-sm"
            :disabled="busy.startsWith(ws.name)"
            @click="action(ws, 'start')"
          >
            Start
          </button>
          <button
            v-if="ws.phase === 'Running' || ws.phase === 'Provisioning'"
            class="btn btn-sm"
            :disabled="busy.startsWith(ws.name)"
            @click="action(ws, 'stop')"
          >
            Stop
          </button>
          <button
            class="btn btn-sm"
            @click="expanded = expanded === ws.name ? null : ws.name"
          >
            {{ expanded === ws.name ? 'Hide connect' : 'Connect' }}
          </button>
          <span style="flex: 1"></span>
          <button class="btn btn-sm btn-danger" :disabled="busy.startsWith(ws.name)" @click="action(ws, 'delete')">
            Delete
          </button>
        </div>

        <div v-if="expanded === ws.name" style="margin-top: 1rem">
          <ConnectionCard :ws="ws" :config="config" :username="auth.user?.username ?? 'user'" />
        </div>
      </div>
    </div>

    <CreateWorkspaceModal
      v-if="showCreate"
      :config="config"
      :images="images"
      @close="showCreate = false"
      @created="showCreate = false; refresh()"
    />
  </div>
</template>