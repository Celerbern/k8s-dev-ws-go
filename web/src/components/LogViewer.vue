<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { ApiError } from '../api'

const props = defineProps<{
  wsName: string
  /** owner override for admins viewing another user's workspace */
  user?: string
  phase: string
  hasGitRepos: boolean
}>()

type Status = 'idle' | 'connecting' | 'streaming' | 'ended' | 'error'

const container = ref('workspace')
const follow = ref(true)
const tail = ref(200) // lines; 0 = all
const output = ref('')
const status = ref<Status>('idle')
const errorMsg = ref('')
const pinned = ref(true)

const logBox = ref<HTMLElement | null>(null)
let controller: AbortController | null = null
let retryTimer: number | undefined

const containers = computed(() => {
  const list = ['workspace']
  if (props.hasGitRepos) list.push('git-clone')
  list.push('fix-permissions')
  return list
})

const active = computed(() => props.phase !== 'Stopped')

function ownerQuery() {
  return props.user ? `&user=${encodeURIComponent(props.user)}` : ''
}

function abort() {
  if (retryTimer) {
    window.clearTimeout(retryTimer)
    retryTimer = undefined
  }
  if (controller) {
    controller.abort()
    controller = null
  }
}

async function append(text: string) {
  output.value += text
  if (pinned.value) {
    await nextTick()
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  }
}

async function connect() {
  abort()
  if (!active.value) {
    status.value = 'idle'
    return
  }
  status.value = 'connecting'
  errorMsg.value = ''
  controller = new AbortController()
  const q = new URLSearchParams({ container: container.value, tail: String(tail.value) })
  if (follow.value) q.set('follow', 'true')
  try {
    const resp = await fetch(`/api/workspaces/${props.wsName}/logs?${q}${ownerQuery()}`, {
      signal: controller.signal,
      credentials: 'same-origin',
    })
    if (!resp.ok || !resp.body) {
      let msg = `${resp.status} ${resp.statusText}`
      try {
        msg = (await resp.json()).error ?? msg
      } catch {
        /* not json */
      }
      throw new ApiError(resp.status, msg)
    }
    status.value = 'streaming'
    const reader = resp.body.getReader()
    const decoder = new TextDecoder()
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      await append(decoder.decode(value, { stream: true }))
    }
    await append(decoder.decode())
    status.value = 'ended'
  } catch (e) {
    if ((e as Error).name === 'AbortError') return
    errorMsg.value = (e as Error).message
    status.value = 'error'
    // While the workspace is starting up the pod may not exist yet — retry.
    if (active.value) retryTimer = window.setTimeout(connect, 3000)
  }
}

function onScroll() {
  const el = logBox.value
  if (!el) return
  pinned.value = el.scrollHeight - el.scrollTop - el.clientHeight < 48
}

function jumpToLatest() {
  pinned.value = true
  if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
}

watch(
  () => [props.wsName, props.user, container.value, tail.value, follow.value],
  () => {
    output.value = ''
    connect()
  },
)
// (Re)connect when the phase changes: starting a stopped workspace creates
// a fresh pod; stopping kills the stream.
watch(
  () => props.phase,
  (phase) => {
    if (phase === 'Stopped') {
      abort()
      status.value = 'idle'
      return
    }
    if (status.value === 'idle' || status.value === 'ended') connect()
  },
)

if (active.value) connect()
onUnmounted(abort)

const statusText = computed(() => {
  switch (status.value) {
    case 'connecting':
      return 'connecting…'
    case 'streaming':
      return follow.value ? 'streaming' : 'complete'
    case 'ended':
      return 'stream ended'
    case 'error':
      return errorMsg.value
    default:
      return 'workspace is stopped — start it to stream logs'
  }
})
</script>

<template>
  <div>
    <div class="row log-controls">
      <label class="inline-label" for="log-container">container</label>
      <select id="log-container" v-model="container" class="narrow">
        <option v-for="c in containers" :key="c" :value="c">{{ c }}</option>
      </select>
      <label class="inline-label" for="log-tail">lines</label>
      <select id="log-tail" v-model.number="tail" class="narrow">
        <option :value="100">100</option>
        <option :value="200">200</option>
        <option :value="500">500</option>
        <option :value="0">all</option>
      </select>
      <button class="btn btn-sm" :class="{ 'btn-primary': follow }" @click="follow = !follow">
        {{ follow ? 'Following' : 'Follow' }}
      </button>
      <button class="btn btn-sm" @click="output = ''">Clear</button>
      <span class="muted" style="margin-left: auto; font-size: 0.82rem">{{ statusText }}</span>
    </div>
    <div class="log-wrap">
      <pre ref="logBox" class="log-box" @scroll="onScroll">{{ output || (status === 'error' ? '' : '(no output yet)') }}</pre>
      <button v-if="!pinned && output" class="btn btn-sm jump-btn" @click="jumpToLatest">↓ latest</button>
    </div>
  </div>
</template>

<style scoped>
.log-controls {
  flex-wrap: wrap;
  margin-bottom: 0.6rem;
}
.inline-label {
  display: inline;
  margin: 0 0 0 0.5rem;
  color: var(--text-dim);
  font-size: 0.82rem;
}
.inline-label:first-child {
  margin-left: 0;
}
.narrow {
  width: auto;
  padding: 0.25rem 0.5rem;
  font-size: 0.85rem;
}
.log-wrap {
  position: relative;
}
.log-box {
  margin: 0;
  max-height: 420px;
  min-height: 180px;
  overflow-y: auto;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.jump-btn {
  position: absolute;
  bottom: 0.75rem;
  right: 0.75rem;
}
</style>