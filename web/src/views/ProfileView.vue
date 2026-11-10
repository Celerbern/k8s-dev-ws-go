<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api, ApiError } from '../api'
import { auth, type User } from '../store'

const me = ref<User | null>(null)
const error = ref('')
const message = ref('')

const keyName = ref('')
const keyValue = ref('')
const tokenName = ref('')
const newToken = ref('')
const currentPassword = ref('')
const newPassword = ref('')

onMounted(async () => {
  me.value = await api.get(`/api/users/${auth.user?.username}`)
})

async function reload() {
  me.value = await api.get(`/api/users/${auth.user?.username}`)
}

async function addKey() {
  error.value = ''
  message.value = ''
  try {
    await api.post(`/api/users/${auth.user?.username}/keys`, {
      name: keyName.value,
      key: keyValue.value,
    })
    keyName.value = ''
    keyValue.value = ''
    message.value = 'SSH key added.'
    await reload()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function removeKey(name: string) {
  error.value = ''
  try {
    await api.del(`/api/users/${auth.user?.username}/keys/${name}`)
    await reload()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function createToken() {
  error.value = ''
  message.value = ''
  newToken.value = ''
  try {
    const resp = await api.post(`/api/users/${auth.user?.username}/tokens`, {
      name: tokenName.value,
    })
    newToken.value = resp.token
    tokenName.value = ''
    await reload()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function revokeToken(name: string) {
  error.value = ''
  try {
    await api.del(`/api/users/${auth.user?.username}/tokens/${name}`)
    await reload()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function changePassword() {
  error.value = ''
  message.value = ''
  try {
    await api.post(`/api/users/${auth.user?.username}/password`, {
      currentPassword: currentPassword.value,
      newPassword: newPassword.value,
    })
    currentPassword.value = ''
    newPassword.value = ''
    message.value = 'Password changed.'
  } catch (e) {
    error.value = (e as ApiError).message
  }
}

async function copy(text: string) {
  await navigator.clipboard.writeText(text)
}
</script>

<template>
  <div v-if="me">
    <h1>Profile</h1>
    <div class="error-banner" v-if="error">{{ error }}</div>
    <p class="muted" v-if="message" style="color: var(--green)">{{ message }}</p>

    <div class="grid" style="grid-template-columns: 1fr 1fr">
      <div class="card">
        <h2>SSH public keys</h2>
        <p class="muted" style="margin-top: 0">
          Keys are used both for the SSH gateway and for logging in to your
          workspaces. Generate one with <code>ssh-keygen -t ed25519</code>.
        </p>
        <table v-if="me.sshPublicKeys?.length">
          <thead>
            <tr><th>Name</th><th>Key</th><th></th></tr>
          </thead>
          <tbody>
            <tr v-for="k in me.sshPublicKeys" :key="k.name">
              <td>{{ k.name }}</td>
              <td class="mono muted">{{ k.type }} {{ k.fingerprint }}</td>
              <td style="text-align: right">
                <button class="btn btn-sm btn-danger" @click="removeKey(k.name)">Remove</button>
              </td>
            </tr>
          </tbody>
        </table>
        <p class="muted" v-else>No keys registered.</p>
        <form @submit.prevent="addKey" style="margin-top: 1rem">
          <div class="field">
            <label for="key-name">Key name</label>
            <input id="key-name" v-model="keyName" placeholder="laptop" required />
          </div>
          <div class="field">
            <label for="key-value">Public key</label>
            <textarea
              id="key-value"
              v-model="keyValue"
              rows="2"
              placeholder="ssh-ed25519 AAAAC3Nza... user@host"
              required
            ></textarea>
          </div>
          <button class="btn btn-primary" :disabled="!keyName || !keyValue">Add key</button>
        </form>
      </div>

      <div>
        <div class="card" style="margin-bottom: 1rem">
          <h2>Quotas</h2>
          <p class="muted" style="margin-top: 0">
            CPU and memory reflect workspaces with a live pod; storage counts all your
            workspaces (home volumes persist while stopped).
          </p>
          <table>
            <thead>
              <tr><th>Resource</th><th>Usage</th><th>Quota</th></tr>
            </thead>
            <tbody>
              <tr>
                <td>Workspaces</td>
                <td>{{ me.usage?.workspaces ?? 0 }} ({{ me.usage?.running ?? 0 }} running)</td>
                <td>{{ me.quotas.maxWorkspaces || '∞' }}</td>
              </tr>
              <tr>
                <td>CPU</td>
                <td>{{ me.usage?.cpu || '0' }}</td>
                <td>{{ me.quotas.cpu || '—' }}</td>
              </tr>
              <tr>
                <td>Memory</td>
                <td>{{ me.usage?.memory || '0' }}</td>
                <td>{{ me.quotas.memory || '—' }}</td>
              </tr>
              <tr>
                <td>Storage</td>
                <td>{{ me.usage?.storage || '0' }}</td>
                <td>{{ me.quotas.storage || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="card" style="margin-bottom: 1rem">
          <h2>API tokens</h2>
          <p class="muted" style="margin-top: 0">
            Used by the <code>devws</code> CLI (<code>devws login &lt;server&gt;</code>).
          </p>
          <table v-if="me.apiTokens?.length">
            <thead>
              <tr><th>Name</th><th>Created</th><th>Expires</th><th></th></tr>
            </thead>
            <tbody>
              <tr v-for="t in me.apiTokens" :key="t.name">
                <td>{{ t.name }}</td>
                <td class="muted">{{ t.createdAt?.slice(0, 10) }}</td>
                <td class="muted">{{ t.expiresAt ? t.expiresAt.slice(0, 10) : 'never' }}</td>
                <td style="text-align: right">
                  <button class="btn btn-sm btn-danger" @click="revokeToken(t.name)">Revoke</button>
                </td>
              </tr>
            </tbody>
          </table>
          <p class="muted" v-else>No tokens.</p>
          <div class="error-banner" v-if="newToken" style="margin-top: 1rem">
            Copy your new token now — it is shown only once:
            <div class="row" style="margin-top: 0.5rem">
              <code class="mono" style="word-break: break-all">{{ newToken }}</code>
              <button class="btn btn-sm" @click="copy(newToken)">Copy</button>
            </div>
          </div>
          <form @submit.prevent="createToken" style="margin-top: 1rem" class="row">
            <input v-model="tokenName" placeholder="token name" required />
            <button class="btn" :disabled="!tokenName">Create token</button>
          </form>
        </div>

        <div class="card">
          <h2>Change password</h2>
          <form @submit.prevent="changePassword">
            <div class="field">
              <label for="cur-pw">Current password</label>
              <input id="cur-pw" type="password" v-model="currentPassword" required />
            </div>
            <div class="field">
              <label for="new-pw">New password</label>
              <input id="new-pw" type="password" v-model="newPassword" minlength="8" required />
            </div>
            <button class="btn btn-primary" :disabled="!currentPassword || !newPassword">
              Change password
            </button>
          </form>
        </div>
      </div>
    </div>
  </div>
</template>