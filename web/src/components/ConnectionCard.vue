<script setup lang="ts">
import { computed, ref } from 'vue'
import type { PlatformConfig, Workspace } from '../store'

const props = defineProps<{
  ws: Workspace
  config: PlatformConfig | null
  username: string
}>()

const tab = ref<'vscode' | 'jetbrains' | 'terminal'>('vscode')
const copied = ref('')

const host = computed(() => props.config?.sshHost ?? 'ssh.devws.local')
const port = computed(() => props.config?.sshPort ?? 22)
const user = computed(() => props.username || 'user')
const alias = computed(() => `devws-${props.ws.name}`)
const gateway = computed(() =>
  port.value === 22 ? `${user.value}@${host.value}` : `${user.value}@${host.value}:${port.value}`,
)

const sshConfig = computed(() => `# devws gateway (authenticates the ProxyJump below)
Host ${host.value}
  Port ${port.value}
  User ${user.value}
  IdentityFile ~/.ssh/id_ed25519

Host ${alias.value}
  HostName ${props.ws.sshHost}
  Port 2222
  User devws
  ProxyJump ${gateway.value}
  ServerAliveInterval 30
  ServerAliveCountMax 4
  StrictHostKeyChecking accept-new
  IdentityFile ~/.ssh/id_ed25519`)

async function copy(text: string, what: string) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = what
    setTimeout(() => {
      if (copied.value === what) copied.value = ''
    }, 1500)
  } catch {
    /* clipboard unavailable (e.g. insecure context) */
  }
}
</script>

<template>
  <div>
    <p class="muted" style="margin-top: 0">
      Add this to <code>~/.ssh/config</code> — or run <code>devws ssh-config --write</code> —
      then connect with your IDE via <code>{{ alias }}</code>.
    </p>
    <div class="copy-box">
      <button class="btn btn-sm" @click="copy(sshConfig, 'config')">
        {{ copied === 'config' ? 'Copied ✓' : 'Copy' }}
      </button>
      <pre>{{ sshConfig }}</pre>
    </div>

    <div class="tabs" style="margin-top: 1rem">
      <button :class="{ active: tab === 'vscode' }" @click="tab = 'vscode'">VS Code</button>
      <button :class="{ active: tab === 'jetbrains' }" @click="tab = 'jetbrains'">JetBrains</button>
      <button :class="{ active: tab === 'terminal' }" @click="tab = 'terminal'">Terminal</button>
    </div>

    <div v-if="tab === 'vscode'" class="ide-guide">
      <ol style="margin: 0; padding-left: 1.2rem">
        <li>Install the <strong>Remote - SSH</strong> extension (ms-vscode-remote.remote-ssh).</li>
        <li>
          Open the command palette (<code>F1</code>) → <em>Remote-SSH: Connect to Host…</em> →
          <code>{{ alias }}</code
          >.
        </li>
        <li>Open folder <code>/home/devws</code>. The remote server downloads on first connect.</li>
      </ol>
      <div class="copy-chip" @click="copy(`code --folder-uri vscode-remote://ssh-remote+${alias}/home/devws`, 'vscode')">
        <code>code --folder-uri vscode-remote://ssh-remote+{{ alias }}/home/devws</code>
        <span class="chip-hint">{{ copied === 'vscode' ? 'copied ✓' : 'copy' }}</span>
      </div>
      <p class="muted" style="margin-bottom: 0">
        Tip: <code>devws code {{ ws.name }}</code> does the same from the CLI.
      </p>
    </div>

    <div v-else-if="tab === 'jetbrains'" class="ide-guide">
      <ol style="margin: 0; padding-left: 1.2rem">
        <li>Install <strong>JetBrains Gateway</strong> (free, from jetbrains.com).</li>
        <li>
          New instance → <strong>SSH connection</strong> → enter host <code>{{ alias }}</code
          >, user <code>devws</code>, port 2222, your key file.
        </li>
        <li>Pick an IDE version, set the project directory to <code>/home/devws</code>.</li>
      </ol>
      <p class="muted" style="margin-bottom: 0">
        The IDE backend lands in <code>~/.cache</code> and <code>~/.local</code> on the
        workspace volume, so it persists across restarts.
      </p>
    </div>

    <div v-else class="ide-guide">
      <div class="copy-chip" @click="copy(`ssh ${alias}`, 'ssh')">
        <code>ssh {{ alias }}</code>
        <span class="chip-hint">{{ copied === 'ssh' ? 'copied ✓' : 'copy' }}</span>
      </div>
      <p class="muted" style="margin-bottom: 0">
        A plain shell — <code>git</code>, <code>go</code>, <code>node</code> and your files are
        all there. VS Code / Gateway sessions run next to it without conflict.
      </p>
    </div>
  </div>
</template>

<style scoped>
.ide-guide {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  font-size: 0.92rem;
}
.copy-chip {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  background: var(--panel-2);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 0.5rem 0.75rem;
  cursor: pointer;
}
.copy-chip:hover {
  background: var(--hover);
}
.copy-chip code {
  background: none;
  border: none;
  padding: 0;
  overflow-wrap: anywhere;
}
.chip-hint {
  color: var(--text-dim);
  font-size: 0.8rem;
  white-space: nowrap;
}
</style>