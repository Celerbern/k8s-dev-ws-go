<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiError } from '../api'
import type { User } from '../store'

const props = defineProps<{ user: User }>()
const emit = defineEmits<{ close: []; saved: [] }>()

const email = ref(props.user.email)
const displayName = ref(props.user.displayName)
const role = ref(props.user.role)
const authMethod = ref(props.user.authMethod || 'local')
const newPassword = ref('')
const maxWorkspaces = ref(props.user.quotas.maxWorkspaces || null)
const cpu = ref(props.user.quotas.cpu || '')
const memory = ref(props.user.quotas.memory || '')
const storage = ref(props.user.quotas.storage || '')
const error = ref('')
const busy = ref(false)

const switchesToLocal = computed(
  () => authMethod.value === 'local' && (props.user.authMethod || 'local') === 'oidc',
)

async function save() {
  error.value = ''
  busy.value = true
  try {
    await api.patch(`/api/users/${props.user.username}`, {
      email: email.value,
      displayName: displayName.value,
      role: role.value,
      authMethod: authMethod.value,
      password: switchesToLocal.value ? newPassword.value : undefined,
      quotas: {
        maxWorkspaces: maxWorkspaces.value ?? 0,
        cpu: cpu.value,
        memory: memory.value,
        storage: storage.value,
      },
    })
    emit('saved')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'failed to save user'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <form class="modal" @submit.prevent="save">
      <h2>Edit {{ user.username }}</h2>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="e-email">Email</label>
        <input id="e-email" type="email" v-model="email" />
      </div>
      <div class="field">
        <label for="e-display">Display name</label>
        <input id="e-display" v-model="displayName" />
      </div>
      <div class="field">
        <label for="e-role">Role</label>
        <select id="e-role" v-model="role">
          <option value="user">user</option>
          <option value="admin">admin</option>
        </select>
      </div>
      <div class="field">
        <label for="e-auth">Authentication</label>
        <select id="e-auth" v-model="authMethod">
          <option value="local">Password</option>
          <option value="oidc">Single sign-on (OIDC)</option>
        </select>
        <p class="muted hint" v-if="authMethod === 'oidc'">
          The username must exactly match the identity provider's username claim;
          switching to SSO removes the user's password.
        </p>
      </div>
      <div class="field" v-if="switchesToLocal">
        <label for="e-password">New password (min 8 chars, required when switching)</label>
        <input id="e-password" type="password" v-model="newPassword" minlength="8" required />
      </div>
      <h2>Quotas</h2>
      <div class="field">
        <label for="e-maxws">Max workspaces</label>
        <input id="e-maxws" type="number" min="0" v-model.number="maxWorkspaces" />
      </div>
      <div class="field">
        <label for="e-cpu">CPU (cores)</label>
        <input id="e-cpu" v-model="cpu" placeholder="8" />
      </div>
      <div class="field">
        <label for="e-memory">Memory</label>
        <input id="e-memory" v-model="memory" placeholder="16Gi" />
      </div>
      <div class="field">
        <label for="e-storage">Storage</label>
        <input id="e-storage" v-model="storage" placeholder="100Gi" />
      </div>
      <div class="modal-actions">
        <button type="button" class="btn" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="busy">Save</button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.hint {
  margin: 0.35rem 0 0;
  font-size: 0.85rem;
}
</style>