<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError } from '../api'
import { auth } from '../store'

const router = useRouter()
const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await api.post('/api/auth/login', {
      username: username.value,
      password: password.value,
    })
    await auth.load()
    router.push('/')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'login failed'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <form class="login-card" @submit.prevent="submit">
      <h1 class="brand">dev<span class="accent">ws</span></h1>
      <p class="muted">Kubernetes development workspaces</p>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="username">Username</label>
        <input id="username" v-model="username" autocomplete="username" autofocus />
      </div>
      <div class="field">
        <label for="password">Password</label>
        <input id="password" type="password" v-model="password" autocomplete="current-password" />
      </div>
      <button class="btn btn-primary" type="submit" :disabled="busy || !username || !password">
        <span v-if="busy" class="spin"></span>
        <span v-else>Sign in</span>
      </button>
    </form>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
}
.login-card {
  width: min(380px, 92vw);
  background: var(--panel);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 2rem;
}
.brand {
  font-size: 1.8rem;
  margin: 0;
  letter-spacing: 0.02em;
}
.accent {
  color: var(--accent);
}
.btn {
  width: 100%;
  justify-content: center;
}
</style>