<script setup>
import { ref, onMounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import QRCode from 'qrcode'

const router = useRouter()

// Auth state
const token = ref(localStorage.getItem('mqtt_admin_token') || '')
const adminMqttUsername = ref('')
const loginForm = ref({ username: '', password: '', totp_code: '', session_days: 7 })
const loginError = ref('')
const loginLoading = ref(false)
const totpEnabled = ref(false)      // 服务端两步验证开关（register/status 下发）
// 2FA 登录弹窗（需要验证码时遮罩弹出，不再在表单里常驻输入框）
const totpLoginModal = ref(false)
const totpLoginDigits = ref(['', '', '', '', '', ''])
const totpLoginMsg = ref('')
const totpLoginLoading = ref(false)

// TOTP 绑定/关闭弹窗
const totpModal = ref(false)
const totpDisableModal = ref(false)
const totpSecret = ref('')
const totpUri = ref('')
const totpCodeInput = ref('')
const totpDisableCode = ref('')
const totpMsg = ref('')
const totpMsgType = ref('')
const totpLoading = ref(false)
const qrCanvas = ref(null)

// Users
const users = ref([])
const usersLoading = ref(false)
const pendingDeleteUser = ref('')
const userActionMsg = ref('')

// ACL editor
const aclContent = ref('')
const aclLoading = ref(false)
const aclSaved = ref(false)

// Config
const configData = ref(null)

// New user form
const newUser = ref({ username: '', password: '', mac: '' })
const newUserMsg = ref('')
const newUserMsgType = ref('')

// Edit password modal
const editModal = ref(false)
const editTarget = ref('')
const editPassword = ref('')
const editMsg = ref('')

// Add MAC modal
const addMacModal = ref(false)
const addMacTarget = ref('')
const addMacValue = ref('')
const addMacFirmware = ref('v1')
const addMacMsg = ref('')
const addMacLoading = ref(false)

// Config edit
const configEditLevel = ref(null)
const configEditGuest = ref(true)
const configEditSkipTotp = ref(false)
const configEditContact = ref('')
const configEditWebhookEnabled = ref(false)
const configEditWebhookURL = ref('')
const configEditWebhookHeaders = ref('')
const configEditWebhookBody = ref('')
const configEditWebhookRetry = ref(1)
const configEditWebhookThrottle = ref(2)
const webhookTesting = ref(false)
const webhookTestMsg = ref('')
const webhookTestMsgType = ref('')
const webhookSaveMsg = ref('')
const webhookSaveMsgType = ref('')
const configSaveMsg = ref('')
const configSaveMsgType = ref('')

// Active tab
const activeTab = ref('users')

const authHeader = () => ({ Authorization: `Bearer ${token.value}` })

const clearAdminSession = () => {
  token.value = ''
  adminMqttUsername.value = ''
  localStorage.removeItem('mqtt_admin_token')
  localStorage.removeItem('mqtt_admin_mqtt_config')
  localStorage.removeItem('mqtt_admin_devices')
  window.dispatchEvent(new CustomEvent('mqtt-admin-logout'))
}

const saveAdminSession = (data) => {
  if (data.token) {
    token.value = data.token
    localStorage.setItem('mqtt_admin_token', data.token)
  }
  localStorage.setItem('mqtt_admin_mqtt_config', JSON.stringify({
    username: data.mqtt_username || '',
    password: data.mqtt_password || ''
  }))
  adminMqttUsername.value = data.mqtt_username || ''
  const devices = saveAdminDevices(data.devices)
  window.dispatchEvent(new CustomEvent('mqtt-admin-devices-updated', { detail: { devices } }))
}

const apiFetch = async (url, opts = {}) => {
  const res = await fetch(url, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...authHeader(), ...(opts.headers || {}) }
  })
  // WAF 拦截页 / 网关 502 等场景返回的不是 JSON，直接 res.json() 会抛异常
  // 导致调用方卡在"加载中"，这里统一容错
  let data = {}
  try {
    data = await res.json()
  } catch (e) {
    data = res.ok ? {} : { error: `请求失败 (HTTP ${res.status})` }
  }
  if (res.status === 401) {
    clearAdminSession()
  }
  return { ok: res.ok, status: res.status, data }
}

const saveAdminDevices = (serverDevices) => {
  let existingDevices = []
  try {
    const parsed = JSON.parse(localStorage.getItem('mqtt_admin_devices') || '[]')
    if (Array.isArray(parsed)) existingDevices = parsed
  } catch (_) {
    existingDevices = []
  }

  const versions = new Map()
  const displayNames = new Map()
  existingDevices.forEach((entry) => {
    const rawMac = typeof entry === 'string' ? entry : entry?.mac
    if (typeof rawMac !== 'string') return
    const mac = rawMac.replace(/[:-]/g, '').toLowerCase()
    if (!/^[0-9a-f]{12}$/.test(mac)) return
    versions.set(mac, typeof entry === 'object' && ['v1', 'v2'].includes(entry?.version)
      ? entry.version
      : 'v1')
    if (typeof entry === 'object' && typeof entry?.display_name === 'string') {
      displayNames.set(mac, entry.display_name)
    }
  })

  const devices = []
  const seen = new Set()
  const addDevice = (entry) => {
    const rawMac = typeof entry === 'string' ? entry : entry?.mac
    if (typeof rawMac !== 'string') return
    const mac = rawMac.replace(/[:-]/g, '').toLowerCase()
    if (!/^[0-9a-f]{12}$/.test(mac) || seen.has(mac)) return

    const serverDisplayName = typeof entry === 'object' && typeof entry?.display_name === 'string'
      ? entry.display_name
      : ''
    devices.push({
      mac,
      version: versions.get(mac) || 'v1',
      display_name: serverDisplayName || displayNames.get(mac) || ''
    })
    seen.add(mac)
  }

  if (Array.isArray(serverDevices)) {
    serverDevices.forEach(device => addDevice(device))
  }
  localStorage.setItem('mqtt_admin_devices', JSON.stringify(devices))
  return devices
}

const login = async (totpCode = '') => {
  loginError.value = ''
  loginLoading.value = true
  try {
    const res = await fetch('/api/admin/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        username: loginForm.value.username,
        password: loginForm.value.password,
        totp_code: totpCode.trim(),
        session_days: loginForm.value.session_days
      })
    })
    const data = await res.json()
    if (res.ok) {
      totpLoginModal.value = false
      saveAdminSession(data)
      window.dispatchEvent(new CustomEvent('mqtt-admin-login'))
      await loadAll()
      router.push('/')
    } else {
      const err = data.error || '登录失败'
      if (err.includes('验证码')) {
        // 外网或未跳过 2FA：弹窗输入，不把错误糊在表单上
        if (!totpLoginModal.value) {
          openTotpLoginModal()
        } else {
          totpLoginMsg.value = err
        }
        loginError.value = ''
      } else {
        loginError.value = err
      }
    }
  } catch (e) {
    loginError.value = '网络错误'
  } finally {
    loginLoading.value = false
    totpLoginLoading.value = false
  }
}

const openTotpLoginModal = () => {
  totpLoginDigits.value = ['', '', '', '', '', '']
  totpLoginMsg.value = ''
  totpLoginModal.value = true
}

const onTotpDigitInput = (i, ev) => {
  const v = String(ev.target.value || '').replace(/\D/g, '').slice(-1)
  totpLoginDigits.value[i] = v
  ev.target.value = v
  if (v && i < 5) {
    document.getElementById(`totp-login-d-${i + 1}`)?.focus()
  }
}

const onTotpDigitKeydown = (i, ev) => {
  if (ev.key === 'Backspace' && !totpLoginDigits.value[i] && i > 0) {
    document.getElementById(`totp-login-d-${i - 1}`)?.focus()
  }
  if (ev.key === 'Enter') submitTotpLogin()
}

const totpLoginCode = () => totpLoginDigits.value.join('')

const submitTotpLogin = async () => {
  const code = totpLoginCode()
  if (code.length !== 6) {
    totpLoginMsg.value = '请输入完整的 6 位动态验证码'
    return
  }
  totpLoginMsg.value = ''
  totpLoginLoading.value = true
  await login(code)
}

const logout = () => {
  clearAdminSession()
}

const refreshSession = async () => {
  const { ok, data } = await apiFetch('/api/admin/session')
  if (!ok) return false
  saveAdminSession(data)
  return true
}

const loadAll = async () => {
  await Promise.all([loadUsers(), loadACL(), loadConfig()])
}

const loadUsers = async () => {
  usersLoading.value = true
  const { ok, data } = await apiFetch('/api/admin/users')
  if (ok) users.value = data || []
  usersLoading.value = false
}

const loadACL = async () => {
  const { ok, data } = await apiFetch('/api/admin/acl')
  if (ok) aclContent.value = data.content || ''
}

const loadConfig = async () => {
  const { ok, data } = await apiFetch('/api/admin/config')
  if (ok) {
    configData.value = data
    configEditLevel.value = data.register_level ?? 1
    configEditGuest.value = data.guest_page_enabled !== false
    configEditSkipTotp.value = data.skip_totp_on_intranet === true
    configEditContact.value = data.contact_info || ''
    configEditWebhookEnabled.value = data.webhook_enabled === true
    configEditWebhookURL.value = data.webhook_url || ''
    configEditWebhookHeaders.value = data.webhook_headers || ''
    configEditWebhookBody.value = data.webhook_body || ''
    configEditWebhookRetry.value = data.webhook_retry ?? 1
    configEditWebhookThrottle.value = data.webhook_throttle_seconds ?? 2
  }
}

const saveACL = async () => {
  aclLoading.value = true
  const { ok, data } = await apiFetch('/api/admin/acl', {
    method: 'PUT',
    body: JSON.stringify({ content: aclContent.value })
  })
  if (ok) {
    aclSaved.value = true
    setTimeout(() => aclSaved.value = false, 2000)
  } else {
    alert(data.error || 'ACL保存失败')
  }
  aclLoading.value = false
}

const deleteUser = async (username) => {
  userActionMsg.value = ''
  const { ok, data } = await apiFetch(`/api/admin/user/${encodeURIComponent(username)}`, { method: 'DELETE' })
  if (ok) {
    pendingDeleteUser.value = ''
    await loadAll()
  } else {
    userActionMsg.value = data.error || '删除失败'
  }
}

const requestDeleteUser = (username) => {
  userActionMsg.value = ''
  pendingDeleteUser.value = username
}

const openEditModal = (username) => {
  editTarget.value = username
  editPassword.value = ''
  editMsg.value = ''
  editModal.value = true
}

const submitEditPassword = async () => {
  if (!editPassword.value) {
    editMsg.value = '密码不能为空'
    return
  }
  const { ok, data } = await apiFetch(`/api/admin/user/${encodeURIComponent(editTarget.value)}`, {
    method: 'PUT',
    body: JSON.stringify({ password: editPassword.value })
  })
  if (ok) {
    editModal.value = false
    editMsg.value = ''
  } else {
    editMsg.value = data.error || '修改失败'
  }
}

const createUser = async () => {
  newUserMsg.value = ''
  const u = newUser.value
  if (!u.username || !u.password || !u.mac) {
    newUserMsg.value = '所有字段不能为空'
    newUserMsgType.value = 'error'
    return
  }
  const macRegex = /^([0-9A-Fa-f]{12}|([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2})$/
  if (!macRegex.test(u.mac)) {
    newUserMsg.value = 'MAC地址格式不正确'
    newUserMsgType.value = 'error'
    return
  }
  const { ok, data } = await apiFetch('/api/admin/users', {
    method: 'POST',
    body: JSON.stringify(u)
  })
  if (ok) {
    newUserMsg.value = '创建成功'
    newUserMsgType.value = 'success'
    newUser.value = { username: '', password: '', mac: '' }
    await loadAll()
  } else {
    newUserMsg.value = data.error || '创建失败'
    newUserMsgType.value = 'error'
  }
}

const saveConfig = async () => {
  configSaveMsg.value = ''
  const level = parseInt(configEditLevel.value)
  if (![0, 1, 2].includes(level)) {
    configSaveMsg.value = '注册等级必须为 0/1/2'
    configSaveMsgType.value = 'error'
    return
  }
  if (configEditWebhookEnabled.value && !configEditWebhookURL.value.trim()) {
    configSaveMsg.value = '已开启 Webhook 时 URL 不能为空'
    configSaveMsgType.value = 'error'
    return
  }
  let retry = parseInt(configEditWebhookRetry.value)
  if (isNaN(retry) || retry < 0) retry = 0
  if (retry > 5) retry = 5
  const { ok, data } = await apiFetch('/api/admin/config', {
    method: 'PUT',
    body: JSON.stringify({
      register_level: level,
      guest_page_enabled: configEditGuest.value,
      skip_totp_on_intranet: configEditSkipTotp.value,
      contact_info: configEditContact.value,
      webhook_enabled: configEditWebhookEnabled.value,
      webhook_url: configEditWebhookURL.value.trim(),
      webhook_headers: configEditWebhookHeaders.value,
      webhook_body: configEditWebhookBody.value,
      webhook_retry: retry,
      webhook_throttle_seconds: Math.min(60, Math.max(0, parseInt(configEditWebhookThrottle.value) || 0))
    })
  })
  if (ok) {
    configData.value.register_level = level
    configData.value.guest_page_enabled = configEditGuest.value
    configData.value.skip_totp_on_intranet = configEditSkipTotp.value
    configData.value.contact_info = configEditContact.value
    configData.value.webhook_enabled = configEditWebhookEnabled.value
    configData.value.webhook_url = configEditWebhookURL.value.trim()
    configData.value.webhook_headers = configEditWebhookHeaders.value
    configData.value.webhook_body = configEditWebhookBody.value
    configData.value.webhook_retry = retry
    configData.value.webhook_throttle_seconds = Math.min(60, Math.max(0, parseInt(configEditWebhookThrottle.value) || 0))
    configSaveMsg.value = '已保存'
    configSaveMsgType.value = 'success'
    setTimeout(() => configSaveMsg.value = '', 2000)
  } else {
    configSaveMsg.value = data.error || '配置修改失败'
    configSaveMsgType.value = 'error'
  }
}

const testWebhook = async () => {
  webhookTestMsg.value = ''
  if (!configEditWebhookURL.value.trim()) {
    webhookTestMsg.value = '请先填写 URL'
    webhookTestMsgType.value = 'error'
    return
  }
  // 先把当前表单写入服务端，再测（测试接口读的是运行时配置）
  const { ok: saveOk, data: saveData } = await apiFetch('/api/admin/config', {
    method: 'PUT',
    body: JSON.stringify({
      webhook_enabled: configEditWebhookEnabled.value,
      webhook_url: configEditWebhookURL.value.trim(),
      webhook_headers: configEditWebhookHeaders.value,
      webhook_body: configEditWebhookBody.value,
      webhook_retry: Math.min(5, Math.max(0, parseInt(configEditWebhookRetry.value) || 0)),
      webhook_throttle_seconds: Math.min(60, Math.max(0, parseInt(configEditWebhookThrottle.value) || 0))
    })
  })
  if (!saveOk) {
    webhookTestMsg.value = saveData.error || '保存配置失败，无法测试'
    webhookTestMsgType.value = 'error'
    return
  }
  webhookTesting.value = true
  const { ok, data } = await apiFetch('/api/admin/webhook/test', { method: 'POST' })
  webhookTesting.value = false
  if (ok) {
    webhookTestMsg.value = data.message || '测试推送成功'
    webhookTestMsgType.value = 'success'
  } else {
    webhookTestMsg.value = data.error || '测试推送失败'
    webhookTestMsgType.value = 'error'
  }
}

const saveWebhookOnly = async () => {
  webhookSaveMsg.value = ''
  if (configEditWebhookEnabled.value && !configEditWebhookURL.value.trim()) {
    webhookSaveMsg.value = '已开启 Webhook 时 URL 不能为空'
    webhookSaveMsgType.value = 'error'
    return
  }
  let retry = parseInt(configEditWebhookRetry.value)
  if (isNaN(retry) || retry < 0) retry = 0
  if (retry > 5) retry = 5
  const throttle = Math.min(60, Math.max(0, parseInt(configEditWebhookThrottle.value) || 0))
  const { ok, data } = await apiFetch('/api/admin/config', {
    method: 'PUT',
    body: JSON.stringify({
      webhook_enabled: configEditWebhookEnabled.value,
      webhook_url: configEditWebhookURL.value.trim(),
      webhook_headers: configEditWebhookHeaders.value,
      webhook_body: configEditWebhookBody.value,
      webhook_retry: retry,
      webhook_throttle_seconds: throttle
    })
  })
  if (ok) {
    configData.value.webhook_enabled = configEditWebhookEnabled.value
    configData.value.webhook_url = configEditWebhookURL.value.trim()
    configData.value.webhook_headers = configEditWebhookHeaders.value
    configData.value.webhook_body = configEditWebhookBody.value
    configData.value.webhook_retry = retry
    configData.value.webhook_throttle_seconds = throttle
    webhookSaveMsg.value = '已保存'
    webhookSaveMsgType.value = 'success'
    setTimeout(() => webhookSaveMsg.value = '', 2000)
  } else {
    webhookSaveMsg.value = data.error || '保存失败'
    webhookSaveMsgType.value = 'error'
  }
}

// ---- 两步验证（TOTP）----
const openTotpSetup = async () => {
  const { ok, data } = await apiFetch('/api/admin/totp/setup', { method: 'POST' })
  if (!ok) {
    alert(data.error || '生成密钥失败')
    return
  }
  totpSecret.value = data.secret
  totpUri.value = data.otpauth_uri
  totpCodeInput.value = ''
  totpMsg.value = ''
  totpModal.value = true
  await nextTick()
  if (qrCanvas.value) {
    QRCode.toCanvas(qrCanvas.value, totpUri.value, { width: 200, margin: 2 }, (err) => {
      if (err) console.error(err)
    })
  }
}

const confirmTotpEnable = async () => {
  totpLoading.value = true
  const { ok, data } = await apiFetch('/api/admin/totp/enable', {
    method: 'POST',
    body: JSON.stringify({ secret: totpSecret.value, code: totpCodeInput.value.trim() })
  })
  totpLoading.value = false
  if (ok) {
    totpModal.value = false
    totpEnabled.value = true
    if (configData.value) configData.value.totp_enabled = true
  } else {
    totpMsg.value = data.error || '验证失败'
    totpMsgType.value = 'error'
  }
}

const confirmTotpDisable = async () => {
  totpLoading.value = true
  const { ok, data } = await apiFetch('/api/admin/totp/disable', {
    method: 'POST',
    body: JSON.stringify({ code: totpDisableCode.value.trim() })
  })
  totpLoading.value = false
  if (ok) {
    totpDisableModal.value = false
    totpDisableCode.value = ''
    totpEnabled.value = false
    if (configData.value) configData.value.totp_enabled = false
  } else {
    totpMsg.value = data.error || '验证失败'
    totpMsgType.value = 'error'
  }
}

const openAddMacModal = (username) => {
  addMacTarget.value = username
  addMacValue.value = ''
  addMacFirmware.value = 'v1'
  addMacMsg.value = ''
  addMacModal.value = true
}

const submitAddMac = async () => {
  addMacMsg.value = ''
  const mac = addMacValue.value.trim()
  const macRegex = /^([0-9A-Fa-f]{12}|([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2})$/
  if (!mac || !macRegex.test(mac)) {
    addMacMsg.value = 'MAC地址格式不正确'
    return
  }
  addMacLoading.value = true
  const { ok, data } = await apiFetch(`/api/admin/user/${encodeURIComponent(addMacTarget.value)}`, {
    method: 'PUT',
    body: JSON.stringify({ mac })
  })
  if (ok) {
    addMacModal.value = false
    await Promise.all([loadAll(), refreshSession()])
    // 追加到管理员自己账号时, 把固件版本持久化到服务端 device-config, 保证跨端一致
    if (addMacFirmware.value === 'v2' && addMacTarget.value === adminMqttUsername.value) {
      const norm = mac.replace(/[:-]/g, '').toLowerCase()
      await apiFetch(`/api/admin/device-config/${encodeURIComponent(norm)}`, {
        method: 'PUT',
        body: JSON.stringify({ firmware: 'v2' })
      })
    }
  } else {
    addMacMsg.value = data.error || '添加失败'
  }
  addMacLoading.value = false
}

onMounted(async () => {
  // 登录页需提前知道是否开启了两步验证（公开接口，只返回开关不返回密钥）
  fetch('/api/register/status', { cache: 'no-store' })
    .then(r => r.json())
    .then(d => { totpEnabled.value = d.totp_enabled === true })
    .catch(() => {})

  if (!token.value) return
  if (await refreshSession()) {
    await loadAll()
  }
})
</script>

<template>
  <div class="container">
    <div class="header-bar">
      <h1>系统管理</h1>
      <div class="header-actions">
        <router-link to="/" class="nav-link">MQTT 控制台</router-link>
        <router-link to="/register" class="nav-link">MQTT 用户注册</router-link>
        <button v-if="token" class="logout-btn" @click="logout">退出</button>
      </div>
    </div>

    <!-- Login -->
    <div v-if="!token" class="login-card">
      <h2>管理员登录</h2>
      <p class="admin-login-hint">登录后，MQTT 控制台会自动加载与管理员账号关联的连接配置。</p>
      <div class="form-group">
        <label>用户名</label>
        <input v-model="loginForm.username" placeholder="管理员用户名" @keyup.enter="login('')" />
      </div>
      <div class="form-group">
        <label>密码</label>
        <input v-model="loginForm.password" type="password" placeholder="管理员密码" @keyup.enter="login('')" />
      </div>
      <!-- 2FA 不再常驻表单：密码正确且服务器要求时弹出验证码框 -->
      <div class="form-group">
        <label>登录有效期</label>
        <select v-model.number="loginForm.session_days" class="config-select">
          <option :value="1">1 天</option>
          <option :value="7">7 天</option>
          <option :value="30">1 个月（30 天）</option>
          <option :value="180">半年（180 天）</option>
          <option :value="365">一年（365 天）</option>
        </select>
      </div>
      <div v-if="loginError" class="msg error">{{ loginError }}</div>
      <p v-if="totpEnabled" class="admin-login-hint" style="font-size:0.85em;color:var(--text-muted)">
        本账号已开启两步验证；登录时若服务器要求，会弹出验证码窗口。
      </p>
      <button class="submit-btn" @click="login('')" :disabled="loginLoading">
        {{ loginLoading ? '登录中...' : '登录' }}
      </button>
    </div>

    <!-- Admin Panel -->
    <div v-else class="admin-panel">
      <!-- Tabs -->
      <div class="tabs">
        <button :class="['tab', { active: activeTab === 'users' }]" @click="activeTab = 'users'">用户列表</button>
        <button :class="['tab', { active: activeTab === 'create' }]" @click="activeTab = 'create'">创建用户</button>
        <button :class="['tab', { active: activeTab === 'acl' }]" @click="activeTab = 'acl'">ACL编辑</button>
        <button :class="['tab', { active: activeTab === 'config' }]" @click="activeTab = 'config'">配置</button>
        <button :class="['tab', { active: activeTab === 'webhook' }]" @click="activeTab = 'webhook'">Webhook</button>
      </div>

      <!-- Users Tab -->
      <div v-if="activeTab === 'users'" class="tab-content">
        <div class="tab-header">
          <h3>用户列表</h3>
          <button class="refresh-btn" @click="loadUsers">刷新</button>
        </div>
        <div v-if="usersLoading" class="loading">加载中...</div>
        <div v-else-if="!users || users.length === 0" class="empty">暂无用户</div>
        <table v-else class="user-table">
          <thead>
            <tr>
              <th>用户名</th>
              <th>授权设备</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="u in users" :key="u.username">
              <td class="username-cell">
                {{ u.username }}
                <span v-if="u.username === adminMqttUsername" class="pattern-tag">当前管理员绑定</span>
              </td>
              <td>
                <span v-if="u.devices && u.devices.length">
                  <span v-for="mac in u.devices" :key="mac" class="pattern-tag">{{ mac }}</span>
                </span>
                <span v-else>暂无设备</span>
              </td>
              <td class="action-cell">
                <button class="edit-btn" @click="openEditModal(u.username)">改密码</button>
                <button class="mac-btn" @click="openAddMacModal(u.username)">追加MAC</button>
                <button class="del-btn" @click="requestDeleteUser(u.username)">删除</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-if="userActionMsg" class="msg error user-action-msg">{{ userActionMsg }}</div>
      </div>

      <!-- Create User Tab -->
      <div v-if="activeTab === 'create'" class="tab-content">
        <h3>创建新用户</h3>
        <div class="form-group">
          <label>用户名</label>
          <input v-model="newUser.username" placeholder="用户名" />
        </div>
        <div class="form-group">
          <label>密码</label>
          <input v-model="newUser.password" type="password" placeholder="密码" />
        </div>
        <div class="form-group">
          <label>设备MAC地址</label>
          <input v-model="newUser.mac" placeholder="D0BAE4618631 或 AA:BB:CC:DD:EE:FF" />
        </div>
        <div v-if="newUserMsg" :class="['msg', newUserMsgType]">{{ newUserMsg }}</div>
        <button class="submit-btn" @click="createUser">创建</button>
      </div>

      <!-- ACL Tab -->
      <div v-if="activeTab === 'acl'" class="tab-content">
        <div class="tab-header">
          <h3>ACL 文件编辑</h3>
          <button class="refresh-btn" @click="loadACL">刷新</button>
        </div>
        <p class="hint">每个用户的规则以 <code>user &lt;用户名&gt;</code> 开头，后跟 <code>topic readwrite &lt;主题&gt;</code></p>
        <textarea
          v-model="aclContent"
          class="acl-editor"
          spellcheck="false"
        ></textarea>
        <div class="acl-actions">
          <button class="submit-btn" @click="saveACL" :disabled="aclLoading">
            {{ aclLoading ? '保存中...' : (aclSaved ? '✓ 已保存' : '保存并重载') }}
          </button>
        </div>
      </div>

      <!-- Config Tab -->
      <div v-if="activeTab === 'config'" class="tab-content">
        <h3>运行时配置</h3>
        <div v-if="configData">
          <div class="config-item">
            <span class="config-label">容器名称</span>
            <span class="config-value">{{ configData.container_name }}</span>
          </div>
          <div class="config-item">
            <span class="config-label">容器内passwd路径</span>
            <span class="config-value">{{ configData.container_passwd_path }}</span>
          </div>
          <div class="config-item">
            <span class="config-label">主机passwd路径</span>
            <span class="config-value">{{ configData.host_passwd_path }}</span>
          </div>
          <div class="config-item">
            <span class="config-label">ACL文件路径</span>
            <span class="config-value">{{ configData.acl_path }}</span>
          </div>
          <div class="config-item">
            <span class="config-label">ACL模式</span>
            <span class="config-value">
              <span v-for="p in configData.acl_patterns" :key="p" class="pattern-tag">{{ p }}</span>
            </span>
          </div>
          <div class="config-item">
            <span class="config-label">注册等级</span>
            <span class="config-value">
              <select v-model="configEditLevel" class="config-select">
                <option :value="0">0 — 禁止注册</option>
                <option :value="1">1 — 任意注册</option>
                <option :value="2">2 — 邮箱验证</option>
              </select>
            </span>
          </div>
          <div class="config-item">
            <span class="config-label">两步验证</span>
            <span class="config-value">
              <template v-if="totpEnabled">
                <span class="pattern-tag">已开启</span>
                <button class="del-btn-inline" @click="totpDisableModal = true; totpDisableCode = ''; totpMsg = ''">关闭</button>
              </template>
              <button v-else class="edit-btn" @click="openTotpSetup">启用两步验证</button>
            </span>
          </div>
          <div class="config-item" v-if="totpEnabled">
            <span class="config-label">内网跳过验证</span>
            <span class="config-value">
              <select v-model="configEditSkipTotp" class="config-select">
                <option :value="true">开启 — 内网登录免输入动态码</option>
                <option :value="false">关闭 — 所有登录均需动态码</option>
              </select>
            </span>
          </div>
          <div class="config-item">
            <span class="config-label">访客页面</span>
            <span class="config-value">
              <select v-model="configEditGuest" class="config-select">
                <option :value="true">开启 — 允许 MQTT 用户使用控制台</option>
                <option :value="false">关闭 — 仅管理员可登录控制台</option>
              </select>
            </span>
          </div>
          <div class="config-item">
            <span class="config-label">联系方式</span>
            <span class="config-value" style="font-family:inherit">
              <input v-model="configEditContact" class="config-input" placeholder="如需帮助请联系管理员" />
            </span>
          </div>
          <div style="margin-top:14px">
            <div v-if="configSaveMsg" :class="['msg', configSaveMsgType]" style="margin-bottom:8px">{{ configSaveMsg }}</div>
            <button class="submit-btn" style="width:auto;padding:8px 24px" @click="saveConfig">保存配置</button>
          </div>
        </div>
        <div v-else class="loading">加载中...</div>
      </div>

      <!-- Webhook Tab -->
      <div v-if="activeTab === 'webhook'" class="tab-content">
        <div class="tab-header">
          <h3>Webhook 通知</h3>
        </div>
        <div v-if="configData">
          <p class="webhook-hint">插口开关翻转时向 URL 发送 HTTP POST（不向排插下发命令）。</p>

          <label class="webhook-field-label">开启</label>
          <select v-model="configEditWebhookEnabled" class="config-select webhook-select">
            <option :value="true">开启 — 翻转时推送</option>
            <option :value="false">关闭</option>
          </select>

          <label class="webhook-field-label">URL</label>
          <input v-model="configEditWebhookURL" class="webhook-input" placeholder="https://example.com/notify?text=${message}" />

          <label class="webhook-field-label">Header（每行一条）</label>
          <textarea v-model="configEditWebhookHeaders" class="webhook-textarea" rows="5"
            placeholder="Authorization: Bearer xxx&#10;X-Token: 123456"></textarea>

          <label class="webhook-field-label">Body 模板（POST JSON）</label>
          <textarea v-model="configEditWebhookBody" class="webhook-textarea" rows="10"
            placeholder='{"group_id":123,"message":"${message}"}'></textarea>

          <div class="webhook-row">
            <div class="webhook-row-item">
              <label class="webhook-field-label">失败重试</label>
              <select v-model.number="configEditWebhookRetry" class="config-select">
                <option :value="0">0 — 不重试</option>
                <option :value="1">1</option>
                <option :value="2">2</option>
                <option :value="3">3</option>
              </select>
            </div>
            <div class="webhook-row-item">
              <label class="webhook-field-label">防刷屏合并</label>
              <select v-model.number="configEditWebhookThrottle" class="config-select">
                <option :value="0">0 — 不合并（每次翻转都推）</option>
                <option :value="1">1 秒窗口</option>
                <option :value="2">2 秒窗口（推荐）</option>
                <option :value="5">5 秒窗口</option>
              </select>
            </div>
          </div>

          <div class="webhook-actions">
            <button class="submit-btn webhook-btn" @click="saveWebhookOnly">保存 Webhook</button>
            <button class="submit-btn webhook-btn webhook-btn-secondary" @click="testWebhook" :disabled="webhookTesting">
              {{ webhookTesting ? '测试中...' : '测试推送' }}
            </button>
          </div>
          <div v-if="webhookSaveMsg" :class="['msg', webhookSaveMsgType]" style="margin-top:10px">{{ webhookSaveMsg }}</div>
          <div v-if="webhookTestMsg" :class="['msg', webhookTestMsgType]" style="margin-top:10px">{{ webhookTestMsg }}</div>
        </div>
        <div v-else class="loading">加载中...</div>
      </div>
    </div>

    <!-- Delete User Modal -->
    <div v-if="pendingDeleteUser" class="modal-overlay" @click.self="pendingDeleteUser = ''">
      <div class="modal">
        <h3>删除 MQTT 用户</h3>
        <p class="delete-confirm-text">确定删除用户 {{ pendingDeleteUser }} 及其 ACL 规则吗？此操作无法撤销。</p>
        <div class="modal-actions">
          <button class="del-confirm-btn" @click="deleteUser(pendingDeleteUser)">确认删除</button>
          <button class="cancel-btn" @click="pendingDeleteUser = ''">取消</button>
        </div>
      </div>
    </div>

    <!-- Add MAC Modal -->
    <div v-if="addMacModal" class="modal-overlay" @click.self="addMacModal = false">
      <div class="modal">
        <h3>追加 MAC 地址 — {{ addMacTarget }}</h3>
        <div class="form-group">
          <label>MAC 地址</label>
          <input v-model="addMacValue" placeholder="D0BAE4618631 或 AA:BB:CC:DD:EE:FF" @keyup.enter="submitAddMac" />
        </div>
        <div class="form-group">
          <label>排插固件版本</label>
          <div class="firmware-options">
            <button type="button" :class="{ active: addMacFirmware === 'v1' }" @click="addMacFirmware = 'v1'">V1 固件</button>
            <button type="button" :class="{ active: addMacFirmware === 'v2' }" @click="addMacFirmware = 'v2'">V2 固件</button>
          </div>
          <p v-if="addMacTarget !== adminMqttUsername" class="hint">
            追加到他人账号：固件版本由该用户在自己控制台添加/切换时设置，此处不影响。
          </p>
        </div>
        <div v-if="addMacMsg" class="msg error">{{ addMacMsg }}</div>
        <div class="modal-actions">
          <button class="submit-btn" @click="submitAddMac" :disabled="addMacLoading">{{ addMacLoading ? '提交中...' : '确认追加' }}</button>
          <button class="cancel-btn" @click="addMacModal = false">取消</button>
        </div>
      </div>
    </div>

    <!-- TOTP 绑定弹窗 -->
    <div v-if="totpModal" class="modal-overlay" @click.self="totpModal = false">
      <div class="modal">
        <h3>启用两步验证</h3>
        <p class="totp-hint">1. 用 Google/Microsoft Authenticator 等扫码添加账号</p>
        <div class="qr-wrap"><canvas ref="qrCanvas"></canvas></div>
        <p class="totp-hint">2. 密钥（请截图备份，换手机可重新扫码）：</p>
        <p class="totp-secret">{{ totpSecret }}</p>
        <div class="form-group">
          <label>输入手机上当前的 6 位动态码确认</label>
          <input
            v-model="totpCodeInput"
            inputmode="numeric"
            maxlength="6"
            placeholder="6 位动态码"
            @keyup.enter="confirmTotpEnable"
          />
        </div>
        <div v-if="totpMsg" class="msg error">{{ totpMsg }}</div>
        <div class="modal-actions">
          <button class="submit-btn" @click="confirmTotpEnable" :disabled="totpLoading">
            {{ totpLoading ? '验证中...' : '确认开启' }}
          </button>
          <button class="cancel-btn" @click="totpModal = false">取消</button>
        </div>
      </div>
    </div>

    <!-- TOTP 关闭弹窗 -->
    <div v-if="totpDisableModal" class="modal-overlay" @click.self="totpDisableModal = false">
      <div class="modal">
        <h3>关闭两步验证</h3>
        <p class="totp-hint">为防止他人误关，请输入当前动态码确认</p>
        <div class="form-group">
          <label>动态验证码</label>
          <input
            v-model="totpDisableCode"
            inputmode="numeric"
            maxlength="6"
            placeholder="6 位动态码"
            @keyup.enter="confirmTotpDisable"
          />
        </div>
        <div v-if="totpMsg" class="msg error">{{ totpMsg }}</div>
        <div class="modal-actions">
          <button class="del-confirm-btn" @click="confirmTotpDisable" :disabled="totpLoading">
            {{ totpLoading ? '验证中...' : '确认关闭' }}
          </button>
          <button class="cancel-btn" @click="totpDisableModal = false">取消</button>
        </div>
      </div>
    </div>

    <!-- Edit Password Modal -->
    <div v-if="editModal" class="modal-overlay" @click.self="editModal = false">
      <div class="modal">
        <h3>修改密码 - {{ editTarget }}</h3>
        <div class="form-group">
          <label>新密码</label>
          <input v-model="editPassword" type="password" placeholder="输入新密码" @keyup.enter="submitEditPassword" />
        </div>
        <div v-if="editMsg" class="msg error">{{ editMsg }}</div>
        <div class="modal-actions">
          <button class="submit-btn" @click="submitEditPassword">确认修改</button>
          <button class="cancel-btn" @click="editModal = false">取消</button>
        </div>
      </div>
    </div>

    <!-- 2FA 登录弹窗：模糊遮罩 + 6 位数字 -->
    <div v-if="totpLoginModal" class="totp-login-overlay" @click.self="totpLoginModal = false">
      <div class="totp-login-modal" role="dialog" aria-modal="true">
        <div class="totp-shield" aria-hidden="true">🛡</div>
        <h3>需要2FA验证</h3>
        <p class="totp-login-hint">请输入2FA验证码</p>
        <div class="totp-digits">
          <input
            v-for="(_, i) in 6"
            :id="`totp-login-d-${i}`"
            :key="i"
            class="totp-digit"
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="1"
            :value="totpLoginDigits[i]"
            @input="onTotpDigitInput(i, $event)"
            @keydown="onTotpDigitKeydown(i, $event)"
          />
        </div>
        <div v-if="totpLoginMsg" class="msg error">{{ totpLoginMsg }}</div>
        <div class="modal-actions">
          <button class="cancel-btn" @click="totpLoginModal = false" :disabled="totpLoginLoading">取消</button>
          <button class="submit-btn" @click="submitTotpLogin" :disabled="totpLoginLoading">
            {{ totpLoginLoading || loginLoading ? '验证中...' : '登录' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.container {
  max-width: 1100px;
  margin: 0 auto;
  padding: 20px;
}
h1 {
  text-align: center;
  color: var(--text-heading);
  margin: 0;
}
.header-bar {
  position: relative;
  display: flex;
  justify-content: center;
  align-items: center;
  margin-bottom: 20px;
  flex-wrap: wrap;
  gap: 10px;
}
.header-actions {
  display: flex;
  gap: 4px;
  align-items: center;
}

/* Mobile: header wraps to separate line */
@media (max-width: 600px) {
  .container {
    padding: 10px;
  }
  .header-bar {
    flex-direction: column;
    align-items: stretch;
  }
  .header-bar h1 {
    margin-bottom: 10px;
  }
  .header-actions {
    justify-content: center;
    flex-wrap: wrap;
  }
  .login-card, .admin-panel {
    margin: 20px auto;
    padding: 20px 14px;
  }
  .tabs {
    flex-wrap: wrap;
  }
  .tab {
    flex: 1 1 auto;
  }
  .tab-content {
    overflow-x: auto;
  }
  .modal-overlay {
    padding: 12px;
  }
  .modal {
    width: 100%;
    min-width: 0;
    padding: 20px;
  }
  .config-item {
    flex-direction: column;
    gap: 6px;
  }
  .config-label,
  .config-value,
  .config-input {
    width: 100%;
    min-width: 0;
  }
}
.nav-link {
  font-size: 0.9em;
  color: var(--text-primary);
  text-decoration: none;
  padding: 4px 10px;
  border-radius: 4px;
  background: var(--bg-secondary);
  transition: background 0.2s;
}
.nav-link:hover {
  background: var(--bg-hover);
}
.logout-btn {
  font-size: 0.9em;
  padding: 4px 10px;
  border-radius: 4px;
  background: #f44336;
  color: white;
  border: none;
  cursor: pointer;
}
.login-card, .admin-panel {
  max-width: 500px;
  margin: 40px auto;
  background: var(--bg-card);
  border-radius: 8px;
  padding: 30px;
  box-shadow: 0 2px 8px var(--shadow-color);
}
.admin-panel {
  max-width: none;
  width: 100%;
  margin: 0;
  padding: 20px 24px;
}
h2, h3 {
  color: var(--text-heading);
  margin: 0 0 16px;
}
.admin-login-hint {
  margin: -6px 0 18px;
  color: var(--text-secondary);
  font-size: 0.9em;
}
.form-group {
  margin-bottom: 16px;
}
.form-group label {
  display: block;
  margin-bottom: 6px;
  font-weight: 500;
  color: var(--text-secondary);
}
.form-group input,
.form-group select {
  width: 100%;
  padding: 10px 12px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 1em;
  box-sizing: border-box;
}
.form-group input:focus,
.form-group select:focus {
  outline: none;
  border-color: #2196f3;
  box-shadow: 0 0 0 2px rgba(33, 150, 243, 0.2);
}
.submit-btn {
  width: 100%;
  padding: 11px;
  background: #2196f3;
  color: white;
  border: none;
  border-radius: 6px;
  font-size: 1em;
  cursor: pointer;
  transition: background 0.2s;
}
.submit-btn:hover:not(:disabled) {
  background: #1976d2;
}
.submit-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.msg {
  padding: 10px;
  border-radius: 6px;
  margin-bottom: 12px;
  font-size: 0.9em;
}
.msg.success { background: #e8f5e9; color: #2e7d32; }
.msg.error { background: #fbe9e7; color: #c62828; }
/* Tabs */
.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 20px;
  border-bottom: 2px solid var(--border-color);
  padding-bottom: 2px;
}
.tab {
  padding: 8px 18px;
  background: none;
  border: none;
  cursor: pointer;
  font-size: 0.95em;
  color: var(--text-secondary);
  border-radius: 6px 6px 0 0;
  transition: all 0.2s;
}
.tab.active {
  color: #2196f3;
  background: var(--bg-secondary);
  font-weight: 600;
}
.tab-content {
  min-height: 300px;
}
.tab-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.tab-header h3 {
  margin: 0;
}
.refresh-btn {
  padding: 5px 12px;
  background: var(--bg-secondary);
  border: 1px solid var(--border-color);
  border-radius: 5px;
  cursor: pointer;
  color: var(--text-primary);
}
.refresh-btn:hover { background: var(--bg-hover); }
/* User table */
.user-table {
  width: 100%;
  border-collapse: collapse;
}
.user-table th, .user-table td {
  padding: 10px 12px;
  text-align: left;
  border-bottom: 1px solid var(--border-light);
}
.user-table th {
  font-weight: 600;
  color: var(--text-secondary);
  background: var(--bg-secondary);
}
.username-cell {
  font-family: monospace;
  font-size: 0.95em;
}
.action-cell {
  display: flex;
  gap: 8px;
}
.edit-btn {
  padding: 4px 10px;
  background: #ff9800;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.85em;
}
.edit-btn:hover { background: #f57c00; }
.mac-btn {
  padding: 4px 10px;
  background: #2196f3;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.85em;
}
.mac-btn:hover { background: #1976d2; }
.del-btn {
  padding: 4px 10px;
  background: #f44336;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.85em;
}
.del-btn:hover { background: #d32f2f; }
/* ACL editor */
.hint {
  font-size: 0.85em;
  color: var(--text-muted);
  margin-bottom: 8px;
}
.firmware-options {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
}
.firmware-options button {
  padding: 8px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-input);
  color: var(--text-primary);
  cursor: pointer;
}
.firmware-options button.active {
  border-color: #2196f3;
  background: var(--bg-active);
  color: var(--text-heading);
  font-weight: 600;
}
.hint code {
  background: var(--bg-secondary);
  padding: 1px 5px;
  border-radius: 3px;
}
.acl-editor {
  width: 100%;
  height: 400px;
  font-family: 'Courier New', monospace;
  font-size: 0.9em;
  padding: 12px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-input);
  color: var(--text-primary);
  resize: vertical;
  box-sizing: border-box;
  line-height: 1.5;
}
.acl-actions {
  margin-top: 12px;
}
/* Config */
.config-item {
  display: flex;
  align-items: flex-start;
  padding: 10px 0;
  border-bottom: 1px solid var(--border-light);
  gap: 16px;
}
.config-label {
  min-width: 160px;
  font-weight: 500;
  color: var(--text-secondary);
  font-size: 0.9em;
}
.config-value {
  color: var(--text-primary);
  font-family: monospace;
  font-size: 0.9em;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}
.pattern-tag {
  background: var(--bg-secondary);
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 0.9em;
}
/* Toggle switch */
.toggle {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
}
.toggle input { opacity: 0; width: 0; height: 0; }
.toggle-slider {
  position: absolute;
  cursor: pointer;
  top: 0; left: 0; right: 0; bottom: 0;
  background: #ccc;
  border-radius: 24px;
  transition: .3s;
}
.toggle-slider::before {
  position: absolute;
  content: "";
  height: 18px; width: 18px;
  left: 3px; bottom: 3px;
  background: white;
  border-radius: 50%;
  transition: .3s;
}
.toggle input:checked + .toggle-slider { background: #4caf50; }
.toggle input:checked + .toggle-slider::before { transform: translateX(20px); }
.config-select {
  padding: 6px 10px;
  border: 1px solid var(--border-color);
  border-radius: 5px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 0.9em;
  cursor: pointer;
}
.config-input {
  padding: 6px 10px;
  border: 1px solid var(--border-color);
  border-radius: 5px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 0.9em;
  width: 280px;
}
/* Webhook 表单：整行铺满，避免 Body 顶出容器 */
.webhook-hint {
  margin: 0 0 16px;
  color: var(--text-secondary, #888);
  font-size: 13px;
  line-height: 1.5;
}
.webhook-field-label {
  display: block;
  margin: 14px 0 6px;
  font-weight: 500;
  color: var(--text-secondary);
  font-size: 0.9em;
}
.webhook-input,
.webhook-textarea {
  display: block;
  width: 100%;
  max-width: 100%;
  box-sizing: border-box;
  padding: 8px 10px;
  border: 1px solid var(--border-color);
  border-radius: 6px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 0.9em;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  line-height: 1.45;
}
.webhook-textarea {
  resize: vertical;
  min-height: 100px;
}
.webhook-select {
  min-width: 200px;
}
.webhook-row {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-start;
  gap: 12px;
  margin-top: 6px;
}
.webhook-row-item {
  min-width: 0;
  width: auto;
}
.webhook-row-item .config-select {
  max-width: 100%;
}
.webhook-actions {
  display: flex;
  gap: 12px;
  margin-top: 20px;
  flex-wrap: wrap;
}
.webhook-btn {
  width: auto;
  min-width: 140px;
  padding: 10px 24px;
}
.webhook-btn-secondary {
  background: var(--bg-secondary, #eee);
  color: var(--text-primary);
  border: 1px solid var(--border-color);
}
.webhook-btn-secondary:hover:not(:disabled) {
  background: var(--bg-hover);
}
/* TOTP */
.del-btn-inline {
  margin-left: 8px;
  padding: 4px 12px;
  border: 1px solid #d32f2f;
  border-radius: 4px;
  background: none;
  color: #d32f2f;
  cursor: pointer;
  font-size: 0.85em;
}
.del-btn-inline:hover { background: #ffebee; }
.qr-wrap {
  display: flex;
  justify-content: center;
  padding: 10px 0;
  background: #fff;
  border-radius: 8px;
  margin: 10px 0;
}
.totp-hint {
  font-size: 0.88em;
  color: var(--text-secondary);
  margin: 6px 0;
  text-align: left;
}
.totp-secret {
  font-family: monospace;
  font-size: 0.9em;
  letter-spacing: 1px;
  background: var(--bg-secondary);
  padding: 8px 10px;
  border-radius: 4px;
  overflow-wrap: anywhere;
  text-align: center;
  margin: 4px 0 12px;
}
/* Modal */
.modal-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  background: rgba(0,0,0,0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}
.modal {
  background: var(--bg-card);
  border-radius: 8px;
  padding: 28px;
  min-width: 360px;
  box-shadow: 0 4px 20px rgba(0,0,0,0.3);
}
.modal-actions {
  display: flex;
  gap: 10px;
}
.modal-actions .submit-btn {
  flex: 1;
}
.delete-confirm-text {
  margin: 0 0 20px;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
}
.del-confirm-btn {
  flex: 1;
  padding: 11px;
  background: #d32f2f;
  color: white;
  border: none;
  border-radius: 6px;
  font-size: 1em;
  cursor: pointer;
}
.user-action-msg {
  margin-top: 12px;
}
.cancel-btn {
  flex: 1;
  padding: 11px;
  background: var(--bg-secondary);
  color: var(--text-primary);
  border: 1px solid var(--border-color);
  border-radius: 6px;
  font-size: 1em;
  cursor: pointer;
}
.loading, .empty {
  text-align: center;
  color: var(--text-muted);
  padding: 40px;
}
/* 2FA 登录弹窗 */
.totp-login-overlay {
  position: fixed;
  inset: 0;
  z-index: 2000;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(15, 23, 42, 0.45);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
  padding: 16px;
}
.totp-login-modal {
  width: min(400px, 100%);
  background: var(--bg-card);
  border-radius: 16px;
  padding: 32px 28px 24px;
  box-shadow: 0 16px 48px rgba(0,0,0,0.28);
  text-align: center;
}
.totp-shield {
  width: 64px;
  height: 64px;
  margin: 0 auto 12px;
  border-radius: 50%;
  background: linear-gradient(145deg, #e8f1ff, #d6e8ff);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.9em;
  line-height: 1;
}
.totp-login-modal h3 {
  margin: 0 0 6px;
  color: var(--text-heading);
}
.totp-login-hint {
  margin: 0 0 20px;
  color: var(--text-muted);
  font-size: 0.95em;
}
.totp-digits {
  display: flex;
  gap: 8px;
  justify-content: center;
  margin-bottom: 16px;
}
.totp-digit {
  width: 44px;
  height: 52px;
  border: 1.5px solid var(--border-color);
  border-radius: 10px;
  background: var(--bg-input);
  color: var(--text-primary);
  text-align: center;
  font-size: 1.35em;
  font-weight: 600;
  outline: none;
}
.totp-digit:focus {
  border-color: #3b82f6;
  box-shadow: 0 0 0 3px rgba(59,130,246,0.2);
}
</style>
