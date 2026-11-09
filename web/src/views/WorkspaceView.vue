<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, ApiError } from '../api'
import { auth, type PlatformConfig, type Workspace, type WorkspaceEvent } from '../store'
import ConnectionCard from '../components/ConnectionCard.vue'
import LogViewer from '../components/LogViewer.vue'

const route = useRoute()
const router = useRouter()

const name = route.params.name as string
const ownerUser = (route.query.user as string) || ''

const ws = ref<Workspace | null>(null)
const config = ref<PlatformConfig | null>(null)
const events = ref<WorkspaceEvent[]>([])
const error = ref('')
const busy = ref(false)
const notFound = ref(false)

let wsTimer: number | undefined
let evTimer: number | undefined

const wsQuery = computed(() => (ownerUser ? `?user=${encodeURIComponent(ownerUser)}` : ''))

async function refresh() {
  try {
    ws.value = await api.get(`/api/workspaces/${name}${wsQuery.value}`)
    notFound.value = false
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) {
      notFound.value = true
      window.clearInterval(wsTimer)
    } else if (e instanceof ApiError && e.status === 401) {
      await auth.load()
    } else {
      error.value = (e as Error).message
    }
  }
}

async function refreshEvents() {
  try {
    events.value = await api.get(`/api/workspaces/${name}/events${wsQuery.value}`)
  } catch {
    /* transient; next poll retries */
  }
}

onMounted(async () => {
  config.value = await api.get('/api/config')
  await refresh()
  await refreshEvents()
  wsTimer = window.setInterval(refresh, 4000)
  evTimer = window.setInterval(refreshEvents, 5000)
})
onUnmounted(() => {
  window.clearInterval(wsTimer)
  window.clearInterval(evTimer)
})

async function action(act: 'start' | 'stop' | 'delete') {
  if (act === 'delete') {
    if (!confirm(`Delete workspace "${name}" including ALL files on its volume?`)) return
    busy.value = true
    try {
      await api.del(`/api/workspaces/${name}${wsQuery.value}`)
      router.push('/')
      return
    } catch (e) {
      error.value = (e as Error).message
    } finally {
      busy.value = false
    }
  }
  busy.value = true
  try {
    await api.post(`/api/workspaces/${name}/${act}${wsQuery.value}`)
    await refresh()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

function phaseClass(phase: string) {
  return {
    Running: 'badge-running',
    Provisioning: 'badge-provisioning',
    Stopped: 'badge-stopped',
    Failed: 'badge-failed',
  }[phase] ?? 'badge-stopped'
}

function timeAgo(iso: string) {
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 60) return `${Math.floor(s)}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}
</script>

<template>
  <div>
    <p style="margin: 0 0 1rem">
      <router-link to="/" class="muted">← Workspaces</router-link>
    </p>

    <div class="error-banner" v-if="error">{{ error }}</div>

    <div class="empty" v-if="notFound">
      <p>Workspace <code>{{ name }}</code> not found.</p>
      <router-link to="/">Back to workspaces</router-link>
    </div>

    <template v-if="ws">
      <div class="spread">
        <div class="row" style="flex-wrap: wrap">
          <h1 style="margin: 0">{{ ws.name }}</h1>
          <span class="badge" :class="phaseClass(ws.phase)">{{ ws.phase }}</span>
          <span class="muted" v-if="ownerUser">· user {{ ownerUser }}</span>
        </div>
        <div class="row">
          <button
            v-if="ws.phase === 'Stopped' || ws.phase === 'Failed'"
            class="btn"
            :disabled="busy"
            @click="action('start')"
          >
            Start
          </button>
          <button
            v-if="ws.phase === 'Running' || ws.phase === 'Provisioning'"
            class="btn"
            :disabled="busy"
            @click="action('stop')"
          >
            Stop
          </button>
          <button class="btn btn-danger" :disabled="busy" @click="action('delete')">Delete</button>
        </div>
      </div>

      <div class="card meta-card">
        <div class="meta-grid">
          <div><span class="muted">class</span><br />{{ ws.class }}</div>
          <div v-if="ws.storage"><span class="muted">storage</span><br />{{ ws.storage }}</div>
          <div><span class="muted">pod</span><br /><span class="mono">{{ ws.podName || '—' }}</span></div>
          <div><span class="muted">created</span><br />{{ timeAgo(ws.createdAt) }}</div>
        </div>
        <p class="muted mono" v-if="ws.message" style="margin: 0.75rem 0 0">{{ ws.message }}</p>
        <p class="muted mono" v-if="ws.gitRepos?.length" style="margin: 0.25rem 0 0">
          {{ ws.gitRepos.join(', ') }}
        </p>
      </div>

      <div class="card section">
        <h2>Connect</h2>
        <ConnectionCard
          :ws="ws"
          :config="config"
          :username="ownerUser || auth.user?.username || 'user'"
        />
      </div>

      <div class="card section">
        <h2>Logs</h2>
        <LogViewer
          :ws-name="ws.name"
          :user="ownerUser || undefined"
          :phase="ws.phase"
          :has-git-repos="!!ws.gitRepos?.length"
        />
      </div>

      <div class="card section">
        <h2>Events</h2>
        <p class="muted" v-if="!events.length" style="margin: 0">No events recorded.</p>
        <table v-else>
          <thead>
            <tr><th>Last seen</th><th>Type</th><th>Reason</th><th>Object</th><th>Message</th></tr>
          </thead>
          <tbody>
            <tr v-for="(ev, i) in [...events].reverse()" :key="i">
              <td class="muted" style="white-space: nowrap">{{ timeAgo(ev.lastSeen) }}</td>
              <td>
                <span
                  class="badge"
                  :class="ev.type === 'Warning' ? 'badge-failed' : 'badge-stopped'"
                >
                  {{ ev.type }}
                </span>
              </td>
              <td>{{ ev.reason }}</td>
              <td class="mono muted">{{ ev.object }}</td>
              <td class="muted">
                {{ ev.message }}<span v-if="ev.count > 1"> (×{{ ev.count }})</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
    <div v-else-if="!notFound" class="row" style="gap: 0.6rem; padding: 2rem 0">
      <span class="spin"></span> <span class="muted">loading…</span>
    </div>
  </div>
</template>

<style scoped>
.meta-card {
  padding: 0.9rem 1.25rem;
  margin-bottom: 1.25rem;
}
.meta-grid {
  display: grid;
  gap: 0.75rem 2rem;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  font-size: 0.92rem;
}
.section {
  margin-bottom: 1.25rem;
}
</style>