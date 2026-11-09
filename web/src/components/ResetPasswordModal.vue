<script setup lang="ts">
import { ref } from 'vue'
import { api, ApiError } from '../api'
import type { User } from '../store'

const props = defineProps<{ user: User }>()
const emit = defineEmits<{ close: [] }>()

const password = ref('')
const error = ref('')
const busy = ref(false)

async function reset() {
  error.value = ''
  busy.value = true
  try {
    await api.post(`/api/users/${props.user.username}/password`, {
      newPassword: password.value,
    })
    emit('close')
  } catch (e) {
    error.value = e instanceof ApiError ? e.message : 'failed to reset password'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <form class="modal" @submit.prevent="reset">
      <h2>Reset password for {{ user.username }}</h2>
      <div class="error-banner" v-if="error">{{ error }}</div>
      <div class="field">
        <label for="rp-pw">New password (min 8 chars)</label>
        <input id="rp-pw" type="password" v-model="password" minlength="8" required />
      </div>
      <div class="modal-actions">
        <button type="button" class="btn" @click="emit('close')">Cancel</button>
        <button type="submit" class="btn btn-primary" :disabled="busy || password.length < 8">
          Reset
        </button>
      </div>
    </form>
  </div>
</template>