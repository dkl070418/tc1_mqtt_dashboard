<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, watch } from 'vue'

const props = defineProps({
  connected: Boolean,
  connectionState: {
    type: String,
    default: 'disconnected'
  },
  connectionError: {
    type: String,
    default: ''
  }
})

const emit = defineEmits(['connect', 'disconnect', 'devices-sync'])

const config = reactive({
  host: 'ws://localhost:8083/mqtt',
  username: '',
  password: '',
  clientId: 'web_client_' + Math.random().toString(16).substr(2, 8)
})

const isExpanded = ref(true)
const showAdvanced = ref(false)
const loginError = ref('')
const loginLoading = ref(false)
const adminManaged = ref(false)

const loadManualConfig = () => {
  config.host = defaultHost.value
  config.username = ''
  config.password = ''
  const saved = localStorage.getItem('mqtt_config')
  if (!saved) return
  try {
    const savedConfig = JSON.parse(saved)
    if (savedConfig.username) config.username = savedConfig.username
    if (savedConfig.password) config.password = savedConfig.password
  } catch (e) {
    console.error('Failed to load MQTT config', e)
  }
}

const statusText = computed(() => {
  if (props.connectionState === 'connecting') return '连接中'
  if (props.connectionState === 'connected') return '已连接'
  if (props.connectionState === 'error') return '连接失败'
  return '未连接'
})

const defaultHost = computed(() => {
  const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${proto}://${window.location.host}/mqtt`
})

const loadAdminConfig = () => {
  const token = localStorage.getItem('mqtt_admin_token')
  const saved = localStorage.getItem('mqtt_admin_mqtt_config')
  adminManaged.value = Boolean(token && saved)
  if (!adminManaged.value) return false
  try {
    const adminConfig = JSON.parse(saved)
    config.username = adminConfig.username || ''
    config.password = adminConfig.password || ''
    config.host = defaultHost.value
    return true
  } catch (e) {
    adminManaged.value = false
    return false
  }
}

const handleAdminLogout = () => {
  adminManaged.value = false
  emit('disconnect')
  loadManualConfig()
}

const handleAdminLogin = () => {
  emit('disconnect')
  if (loadAdminConfig()) {
    handleConnect()
  }
}

onMounted(() => {
  config.host = defaultHost.value
  const hasAdminConfig = loadAdminConfig()
  if (!hasAdminConfig) {
    loadManualConfig()
  }
  if (hasAdminConfig || localStorage.getItem('mqtt_auto_connect') === 'true') {
    handleConnect()
  }
  window.addEventListener('mqtt-admin-login', handleAdminLogin)
  window.addEventListener('mqtt-admin-logout', handleAdminLogout)
})

onUnmounted(() => {
  window.removeEventListener('mqtt-admin-login', handleAdminLogin)
  window.removeEventListener('mqtt-admin-logout', handleAdminLogout)
})

watch(() => props.connectionState, (state) => {
  if (state === 'connected') isExpanded.value = false
  if (state === 'error') isExpanded.value = true
})

const handleConnect = async () => {
  loginError.value = ''
  if (!config.username || !config.password) {
    loginError.value = '请输入 MQTT 用户名和密码'
    return
  }

  loginLoading.value = true
  try {
    const res = await fetch('/api/mqtt/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: config.username, password: config.password })
    })
    const data = await res.json()
    if (!res.ok) {
      loginError.value = data.error || 'MQTT 账号登录失败'
      return
    }
    if (!adminManaged.value) {
      emit('devices-sync', Array.isArray(data.devices) ? data.devices : [])
    }
  } catch (e) {
    loginError.value = '无法验证 MQTT 账号'
    return
  } finally {
    loginLoading.value = false
  }

  if (!adminManaged.value) {
    localStorage.setItem('mqtt_config', JSON.stringify({ username: config.username, password: config.password }))
  }
  localStorage.setItem('mqtt_auto_connect', 'true')
  emit('connect', { ...config })
}

const handleDisconnect = () => {
  localStorage.setItem('mqtt_auto_connect', 'false')
  emit('disconnect')
}

const toggleExpand = () => {
  isExpanded.value = !isExpanded.value
}
</script>

<template>
  <div class="mqtt-config card">
    <div class="header" @click="toggleExpand">
      <h3>
        MQTT 设置 
        <span class="status-indicator" :class="connectionState"></span>
        {{ statusText }}
      </h3>
      <button class="toggle-btn">{{ isExpanded ? '▼' : '▶' }}</button>
    </div>
    
    <div v-if="isExpanded" class="form-body">
      <div v-if="connectionError" class="connection-error">{{ connectionError }}</div>
      <div v-if="loginError" class="connection-error">{{ loginError }}</div>
      <div v-if="adminManaged" class="admin-managed-hint">已加载管理员账号关联的 MQTT 配置</div>
      <div class="form-group">
        <label>MQTT 用户名</label>
        <input v-model="config.username" placeholder="User" :disabled="connected">
      </div>
      <div class="form-group">
        <label>MQTT 密码</label>
        <input v-model="config.password" type="password" placeholder="Password" :disabled="connected">
      </div>
      <button type="button" class="advanced-toggle" @click="showAdvanced = !showAdvanced">
        {{ showAdvanced ? '收起高级设置' : '高级设置' }}
      </button>
      <div v-if="showAdvanced" class="advanced-settings">
        <div class="form-group">
          <label>MQTT 服务器地址</label>
          <input v-model="config.host" :placeholder="defaultHost" :disabled="connected">
          <p class="field-help">默认使用当前站点：{{ defaultHost }}</p>
        </div>
      </div>
       <div class="actions">
        <button v-if="!connected" @click="handleConnect" class="primary-btn" :disabled="connectionState === 'connecting' || loginLoading">
          {{ loginLoading ? '验证账号...' : (connectionState === 'connecting' ? '连接中...' : '登录并连接') }}
        </button>
        <button v-else @click="handleDisconnect" class="danger-btn">断开</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mqtt-config {
  margin-bottom: 20px;
}
.header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  cursor: pointer;
  padding: 10px;
  background: var(--bg-secondary);
  border-radius: 4px;
  color: var(--text-primary);
}
.status-indicator {
  display: inline-block;
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: #ccc;
  margin-left: 10px;
}
.status-indicator.connected {
  background: #4caf50;
}
.status-indicator.connecting {
  background: #ff9800;
}
.status-indicator.error {
  background: #f44336;
}
.connection-error {
  margin-bottom: 15px;
  padding: 10px 12px;
  border-radius: 4px;
  background: var(--chip-danger-hover);
  color: var(--chip-danger-color);
  font-size: 0.9em;
}
.admin-managed-hint {
  margin-bottom: 15px;
  padding: 10px 12px;
  border-radius: 4px;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  font-size: 0.9em;
}
.form-body {
  padding: 15px;
  border: 1px solid var(--border-color);
  border-top: none;
  background: var(--bg-card);
}
.form-group {
  margin-bottom: 15px;
}
.form-group label {
  display: block;
  margin-bottom: 5px;
  color: var(--text-secondary);
}
.form-group input {
  width: 100%;
  padding: 8px;
  box-sizing: border-box;
  background: var(--bg-input);
  color: var(--text-primary);
  border: 1px solid var(--border-color);
  border-radius: 4px;
}
.actions {
  text-align: right;
}
.advanced-toggle {
  margin-bottom: 15px;
  padding: 0;
  border: none;
  background: none;
  color: #2196f3;
}
.advanced-settings {
  padding: 12px;
  margin-bottom: 15px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-secondary);
}
.field-help {
  margin: 5px 0 0;
  color: var(--text-muted);
  font-size: 0.8em;
}
button {
  padding: 8px 16px;
  cursor: pointer;
  border-radius: 4px;
}
.primary-btn {
  background: #4caf50;
  color: white;
  border: none;
}
.primary-btn:disabled {
  cursor: wait;
  opacity: 0.65;
}
.danger-btn {
  background: #f44336;
  color: white;
  border: none;
}
.toggle-btn {
  background: none;
  border: none;
  font-size: 12px;
  color: var(--text-primary);
}
</style>
