<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiError } from '../api'
import type { PlatformConfig, WorkspaceImage } from '../store'

const props = defineProps<{ config: PlatformConfig | null; images: WorkspaceImage[] }>()
const emit = defineEmits<{ close: []; created: [] }>()

const name = ref('')
const image = ref('')
const cls = ref('')
const gitRepos = ref('')
const error = ref('')
const busy = ref(false)

// Only non-deprecated catalog entries are offered; the empty value keeps
// the platform default image.
const selectableImages = computed(() => props.images.filter((i) => !i.deprecated))
const imageHint = computed(() => {
  const selected = selectableImages.value.find((i) => i.image === image.value)
  return selected?.description || ''
})

// shortRef trims a full image reference to its last path element for the
// picker label, e.g. "ghcr.io/acme/devws-python:3.12" -> "devws-python:3.12".
function shortRef(ref: string) {
  return ref.split('/').pop() ?? ref
}

const classNames = computed(() => Object.keys(props.config?.classes ?? {}))
const classHint = computed(() => {
  const c = props.config?.classes[cls.value || props.config?.defaultClass || '']
  return c ? `${c.cpu} CPU · ${c.memory} RAM · ${c.storage} disk` : ''
})

async function create() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/workspaces', {
      name: name.value,
      image: image.value || undefined,
      class: cls.value || undefined,
      gitRepos: gitRepos.value
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean),
    })
    emit('created')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'failed to create workspace'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <form class="modal" @submit.prevent="create">
      <h2>New workspace</h2>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="ws-name">Name</label>
        <input
          id="ws-name"
          v-model="name"
          placeholder="main"
          pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
          title="lowercase letters, digits and dashes"
          required
        />
      </div>
      <div class="field">
        <label for="ws-image">Image</label>
        <select id="ws-image" v-model="image">
          <option value="">Platform default</option>
          <option v-for="img in selectableImages" :key="img.name" :value="img.image">
            {{ img.name }} ({{ shortRef(img.image) }})
          </option>
        </select>
        <p class="muted hint" v-if="imageHint">{{ imageHint }}</p>
      </div>
      <div class="field">
        <label for="ws-class">Class</label>
        <select id="ws-class" v-model="cls">
          <option value="" v-if="config">{{ config.defaultClass }} (default)</option>
          <option v-for="c in classNames" :key="c" :value="c">{{ c }}</option>
        </select>
        <p class="muted" style="margin: 0.35rem 0 0" v-if="classHint">{{ classHint }}</p>
      </div>
      <div class="field">
        <label for="ws-repos">Git repositories to clone (optional, public repos, one URL per line)</label>
        <textarea id="ws-repos" v-model="gitRepos" rows="3" placeholder="https://github.com/org/repo.git"></textarea>
      </div>
      <div class="modal-actions">
        <button type="button" class="btn" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="busy || !name">
          <span v-if="busy" class="spin"></span>
          <span v-else>Create</span>
        </button>
      </div>
    </form>
  </div>
</template>