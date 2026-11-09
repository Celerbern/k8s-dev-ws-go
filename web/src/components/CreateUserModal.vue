<script setup lang="ts">
import { ref } from 'vue'
import { api, ApiError } from '../api'

const emit = defineEmits<{ close: []; created: [] }>()

const username = ref('')
const password = ref('')
const email = ref('')
const displayName = ref('')
const role = ref('user')
const maxWorkspaces = ref<number | null>(null)
const error = ref('')
const busy = ref(false)

async function create() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/users', {
      username: username.value,
      password: password.value,
      email: email.value,
      displayName: displayName.value,
      role: role.value,
      quotas: maxWorkspaces.value ? { maxWorkspaces: maxWorkspaces.value } : undefined,
    })
    emit('created')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'failed to create user'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <form class="modal" @submit.prevent="create">
      <h2>New user</h2>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="u-username">Username</label>
        <input
          id="u-username"
          v-model="username"
          placeholder="jdoe"
          pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
          title="lowercase letters, digits and dashes"
          required
        />
      </div>
      <div class="field">
        <label for="u-password">Initial password (min 8 chars)</label>
        <input id="u-password" type="password" v-model="password" minlength="8" required />
      </div>
      <div class="field">
        <label for="u-email">Email</label>
        <input id="u-email" type="email" v-model="email" placeholder="jdoe@example.com" />
      </div>
      <div class="field">
        <label for="u-display">Display name</label>
        <input id="u-display" v-model="displayName" placeholder="Jane Doe" />
      </div>
      <div class="field">
        <label for="u-role">Role</label>
        <select id="u-role" v-model="role">
          <option value="user">user</option>
          <option value="admin">admin</option>
        </select>
      </div>
      <div class="field">
        <label for="u-maxws">Max workspaces (empty = platform default)</label>
        <input id="u-maxws" type="number" min="1" v-model.number="maxWorkspaces" />
      </div>
      <div class="modal-actions">
        <button type="button" class="btn" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="busy">
          <span v-if="busy" class="spin"></span>
          <span v-else>Create</span>
        </button>
      </div>
    </form>
  </div>
</template>