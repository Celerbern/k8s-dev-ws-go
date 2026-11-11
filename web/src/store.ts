import { reactive } from 'vue'
import { api } from './api'

export interface SSHKey {
  name: string
  type: string
  fingerprint: string
  createdAt?: string
}

export interface Token {
  name: string
  createdAt?: string
  expiresAt?: string
}

export interface Usage {
  workspaces: number
  running: number
  cpu: string
  memory: string
  storage: string
}

export interface User {
  username: string
  email: string
  displayName: string
  role: string
  authMethod: string
  disabled: boolean
  namespace: string
  createdAt: string
  quotas: { maxWorkspaces: number; cpu: string; memory: string; storage: string }
  usage?: Usage
  sshPublicKeys?: SSHKey[]
  apiTokens?: Token[]
  workspaceCount?: number
}

export interface Workspace {
  name: string
  user: string
  namespace: string
  class: string
  image: string
  storage: string
  gitRepos?: string[]
  paused: boolean
  phase: string
  message: string
  podName: string
  sshHost: string
  createdAt: string
}

export interface WorkspaceEvent {
  type: string
  reason: string
  object: string
  message: string
  count: number
  firstSeen: string
  lastSeen: string
}

export interface PlatformConfig {
  sshHost: string
  sshPort: number
  defaultClass: string
  workspaceImage: string
  classes: Record<string, { cpu: string; memory: string; storage: string }>
}

export const auth = reactive({
  user: null as User | null,
  loaded: false,
  async load() {
    try {
      this.user = await api.get('/api/auth/me')
    } catch {
      this.user = null
    }
    this.loaded = true
  },
  async logout() {
    try {
      await api.post('/api/auth/logout')
    } finally {
      this.user = null
    }
  },
})