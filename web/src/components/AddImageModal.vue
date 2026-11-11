<script setup lang="ts">
import { ref } from 'vue'
import { api, ApiError } from '../api'

const emit = defineEmits<{ close: []; created: [] }>()

const name = ref('')
const image = ref('')
const tag = ref('')
const description = ref('')
const error = ref('')
const busy = ref(false)

async function create() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/workspace-images', {
      name: name.value,
      // The server joins image + tag when the image carries no tag.
      image: image.value,
      tag: tag.value || undefined,
      description: description.value || undefined,
    })
    emit('created')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'failed to add image'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <form class="modal" @submit.prevent="create">
      <h2>New workspace image</h2>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="img-name">Name</label>
        <input
          id="img-name"
          v-model="name"
          placeholder="python-3-12"
          pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
          title="lowercase letters, digits and dashes"
          required
        />
      </div>
      <div class="field">
        <label for="img-repo">Image repository</label>
        <input id="img-repo" v-model="image" placeholder="ghcr.io/acme/devws-python" required />
      </div>
      <div class="field">
        <label for="img-tag">Tag (or append it to the repository above, e.g. repo:3.12)</label>
        <input id="img-tag" v-model="tag" placeholder="3.12" pattern="[a-zA-Z0-9._\-]+" />
      </div>
      <div class="field">
        <label for="img-desc">Description (shown in the workspace creation picker)</label>
        <textarea id="img-desc" v-model="description" rows="2" placeholder="Python 3.12 toolchain"></textarea>
      </div>
      <div class="modal-actions">
        <button type="button" class="btn" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="busy || !name || !image">
          <span v-if="busy" class="spin"></span>
          <span v-else>Add</span>
        </button>
      </div>
    </form>
  </div>
</template>