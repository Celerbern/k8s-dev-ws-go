<script setup lang="ts">
import { useRouter } from 'vue-router'
import { auth } from './store'

const router = useRouter()

async function logout() {
  await auth.logout()
  router.push('/login')
}
</script>

<template>
  <div class="shell" v-if="auth.user">
    <header class="topbar">
      <router-link to="/" class="brand">dev<span class="brand-accent">ws</span></router-link>
      <nav class="nav">
        <router-link to="/">Workspaces</router-link>
        <router-link to="/profile">Profile</router-link>
        <router-link to="/admin" v-if="auth.user.role === 'admin'">Admin</router-link>
      </nav>
      <div class="user-menu">
        <span class="user-name">{{ auth.user.displayName || auth.user.username }}</span>
        <button class="btn btn-ghost" @click="logout">Log out</button>
      </div>
    </header>
    <main class="content">
      <router-view />
    </main>
  </div>
  <router-view v-else />
</template>

<style scoped>
.shell {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.topbar {
  display: flex;
  align-items: center;
  gap: 2rem;
  padding: 0.75rem 1.5rem;
  background: var(--panel);
  border-bottom: 1px solid var(--border);
  position: sticky;
  top: 0;
  z-index: 10;
}
.brand {
  font-weight: 700;
  font-size: 1.15rem;
  text-decoration: none;
  color: var(--text);
  letter-spacing: 0.02em;
}
.brand-accent {
  color: var(--accent);
}
.nav {
  display: flex;
  gap: 0.25rem;
  flex: 1;
}
.nav a {
  color: var(--text-dim);
  text-decoration: none;
  padding: 0.4rem 0.9rem;
  border-radius: 8px;
  font-size: 0.95rem;
}
.nav a:hover {
  color: var(--text);
  background: var(--hover);
}
.nav a.router-link-active {
  color: var(--text);
  background: var(--hover);
}
.user-menu {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}
.user-name {
  color: var(--text-dim);
  font-size: 0.9rem;
}
.content {
  flex: 1;
  width: 100%;
  max-width: 1180px;
  margin: 0 auto;
  padding: 1.5rem;
}
</style>