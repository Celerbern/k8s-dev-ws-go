<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import type { User, Workspace } from '../store'
import CreateUserModal from '../components/CreateUserModal.vue'
import EditUserModal from '../components/EditUserModal.vue'
import ResetPasswordModal from '../components/ResetPasswordModal.vue'

const tab = ref<'users' | 'workspaces' | 'usage'>('users')
const users = ref<User[]>([])
const workspaces = ref<Workspace[]>([])
const usage = ref<any>(null)
const error = ref('')
const message = ref('')

const showCreateUser = ref(false)
const editingUser = ref<User | null>(null)
const resettingUser = ref<User | null>(null)

onMounted(refreshAll)

async function refreshAll() {
  error.value = ''
  try {
    const [u, w, s] = await Promise.all([
      api.get('/api/users'),
      api.get('/api/workspaces?scope=all'),
      api.get('/api/admin/usage'),
    ])
    users.value = u
    workspaces.value = w
    usage.value = s
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function toggleDisabled(user: User) {
  error.value = ''
  try {
    await api.patch(`/api/users/${user.username}`, { disabled: !user.disabled })
    await refreshAll()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function deleteUser(user: User) {
  if (!confirm(`Delete user "${user.username}" and ALL their workspaces and data?`)) return
  error.value = ''
  try {
    await api.del(`/api/users/${user.username}`)
    message.value = `User ${user.username} is being deleted.`
    await refreshAll()
  } catch (e) {
    error.value = (e as Error).message
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
</script>

<template>
  <div>
    <h1>Admin</h1>
    <div class="error-banner" v-if="error">{{ error }}</div>
    <p class="muted" v-if="message" style="color: var(--green)">{{ message }}</p>

    <div class="tabs">
      <button :class="{ active: tab === 'users' }" @click="tab = 'users'">Users</button>
      <button :class="{ active: tab === 'workspaces' }" @click="tab = 'workspaces'">
        Workspaces
      </button>
      <button :class="{ active: tab === 'usage' }" @click="tab = 'usage'">Usage</button>
    </div>

    <template v-if="tab === 'users'">
      <div class="spread">
        <p class="muted" style="margin: 0">{{ users.length }} users</p>
        <button class="btn btn-primary" @click="showCreateUser = true">+ New user</button>
      </div>
      <table>
        <thead>
          <tr>
            <th>Username</th><th>Email</th><th>Role</th><th>Workspaces</th><th>Status</th><th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.username">
            <td><strong>{{ u.username }}</strong></td>
            <td class="muted">{{ u.email || '—' }}</td>
            <td>
              <span class="badge" :class="u.role === 'admin' ? 'badge-provisioning' : 'badge-stopped'">
                {{ u.role }}
              </span>
            </td>
            <td>{{ u.workspaceCount }}</td>
            <td>
              <span class="badge" :class="u.disabled ? 'badge-failed' : 'badge-running'">
                {{ u.disabled ? 'disabled' : 'active' }}
              </span>
            </td>
            <td style="text-align: right; white-space: nowrap">
              <button class="btn btn-sm" @click="editingUser = u">Edit</button>
              <button class="btn btn-sm" @click="resettingUser = u">Reset pw</button>
              <button class="btn btn-sm" @click="toggleDisabled(u)">
                {{ u.disabled ? 'Enable' : 'Disable' }}
              </button>
              <button class="btn btn-sm btn-danger" @click="deleteUser(u)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
    </template>

    <template v-if="tab === 'workspaces'">
      <p class="muted">{{ workspaces.length }} workspaces across all users</p>
      <table>
        <thead>
          <tr><th>User</th><th>Name</th><th>Class</th><th>Phase</th><th>Message</th></tr>
        </thead>
        <tbody>
          <tr v-for="ws in workspaces" :key="ws.namespace + '/' + ws.name">
            <td>{{ ws.user }}</td>
            <td>
              <router-link :to="`/workspace/${ws.name}?user=${ws.user}`"><strong>{{ ws.name }}</strong></router-link>
            </td>
            <td class="muted">{{ ws.class }}</td>
            <td><span class="badge" :class="phaseClass(ws.phase)">{{ ws.phase }}</span></td>
            <td class="muted">{{ ws.message }}</td>
          </tr>
        </tbody>
      </table>
    </template>

    <template v-if="tab === 'usage' && usage">
      <div class="grid" style="grid-template-columns: repeat(auto-fill, minmax(200px, 1fr))">
        <div class="card" style="text-align: center">
          <div style="font-size: 2rem; font-weight: 700">{{ usage.users }}</div>
          <div class="muted">users</div>
        </div>
        <div class="card" style="text-align: center">
          <div style="font-size: 2rem; font-weight: 700">{{ usage.workspaces }}</div>
          <div class="muted">workspaces</div>
        </div>
        <div class="card" style="text-align: center">
          <div style="font-size: 2rem; font-weight: 700; color: var(--green)">{{ usage.running }}</div>
          <div class="muted">running</div>
        </div>
      </div>
      <h2 style="margin-top: 1.5rem">Per user</h2>
      <table>
        <thead><tr><th>User</th><th>Workspaces</th><th>Storage</th></tr></thead>
        <tbody>
          <tr v-for="(row, user) in usage.perUser" :key="user">
            <td>{{ user }}</td><td>{{ row.workspaces }}</td><td class="muted">{{ row.storage }}</td>
          </tr>
        </tbody>
      </table>
    </template>

    <CreateUserModal v-if="showCreateUser" @close="showCreateUser = false" @created="showCreateUser = false; refreshAll()" />
    <EditUserModal v-if="editingUser" :user="editingUser" @close="editingUser = null" @saved="editingUser = null; refreshAll()" />
    <ResetPasswordModal v-if="resettingUser" :user="resettingUser" @close="resettingUser = null" />
  </div>
</template>