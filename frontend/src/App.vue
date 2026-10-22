<script setup>
import { ref, reactive, onMounted, onUnmounted, computed, watch } from 'vue'
import { useRouter } from 'vue-router'
import mqtt from 'mqtt'
import MqttConfig from './components/MqttConfig.vue'
import DeviceControl from './components/DeviceControl.vue'
import ImportExport from './components/ImportExport.vue'

// 前端构建版本号(与镜像 tag 同步)
const APP_VERSION = 'v2.1.2'

const router = useRouter()

// State
const client = ref(null)
const connected = ref(false)
const connectionState = ref('disconnected')
const connectionError = ref('')
const devices = reactive({}) // { mac: { ...deviceState, ...localMeta } }
const selectedMac = ref(null)
const newMacInput = ref('')
const deviceList = ref([]) // List of { mac, version } to track
const deviceConfigs = reactive({}) // { mac: { layout, remarks, tasks, version } } 后端持久化的设备配置
const newDeviceVersion = ref('v1')
const addDeviceError = ref('')
const pendingRemoveMac = ref(null)

// Local Storage Keys
const STORAGE_DEVICES = 'ztc1_devices'
const STORAGE_ADMIN_DEVICES = 'mqtt_admin_devices'

const adminManaged = ref(Boolean(
  localStorage.getItem('mqtt_admin_token') && localStorage.getItem('mqtt_admin_mqtt_config')
))
const adminUsername = ref('')

const loadAdminUsername = () => {
  try {
    const raw = localStorage.getItem('mqtt_admin_mqtt_config')
    if (!raw) { adminUsername.value = ''; return }
    const conf = JSON.parse(raw)
    adminUsername.value = conf.username || ''
  } catch (e) {
    adminUsername.value = ''
  }
}
loadAdminUsername()

const logoutAdmin = () => {
  localStorage.removeItem('mqtt_admin_token')
  localStorage.removeItem('mqtt_admin_mqtt_config')
  localStorage.removeItem('mqtt_admin_devices')
  adminUsername.value = ''
  window.dispatchEvent(new CustomEvent('mqtt-admin-logout'))
}

// 管理员会话失效/写回失败的可见反馈。
// 旧行为是只 console.warn：登录令牌过期后页面照样显示"已保存"，刷新即消失
// （实测 10-08 令牌过期后连续 5 天设置的定时任务全部没进服务器）。
let lastAdminNoticeAt = 0
const notifyAdminError = (msg) => {
  const now = Date.now()
  // 60s 节流：倒计时清理等周期任务会连续调用保存，避免弹窗刷屏
  if (now - lastAdminNoticeAt < 60000) return
  lastAdminNoticeAt = now
  alert(msg)
}

let unauthorizedNotified = false
const handleAdminUnauthorized = () => {
  if (unauthorizedNotified) return
  unauthorizedNotified = true
  notifyAdminError('管理员登录已失效（会话到期）。\n刚才的修改只存在浏览器里，没有保存到服务器，刷新就会丢失。\n请重新登录后再设置。')
  logoutAdmin()
}

// 访客页面开关（服务器 /api/register/status 下发）
// false 且未登录管理员 → 整页跳转管理员登录页
// 管理员登录态无需等待，直接渲染
const gateReady = ref(adminManaged.value)

const loadStoredDevices = (key) => {
  const saved = localStorage.getItem(key)
  if (!saved) return []
  try {
    const parsed = JSON.parse(saved)
    if (!Array.isArray(parsed)) return []
    return parsed.map(entry => typeof entry === 'string'
      ? { mac: entry, version: 'v1' }
      : entry
    )
  } catch (e) {
    console.error('Failed to load devices', e)
    return []
  }
}

const clearDeviceRuntimeState = () => {
  Object.keys(devices).forEach(mac => delete devices[mac])
  selectedMac.value = null
  pendingRemoveMac.value = null
}

const switchDeviceSource = (useAdminDevices) => {
  disconnectMqtt()
  adminManaged.value = useAdminDevices
  clearDeviceRuntimeState()
  deviceList.value = loadStoredDevices(useAdminDevices ? STORAGE_ADMIN_DEVICES : STORAGE_DEVICES)
  selectedMac.value = deviceList.value[0]?.mac || null
}

const handleAdminLogin = () => {
  // 重新登录后允许再次提示
  unauthorizedNotified = false
  lastAdminNoticeAt = 0
  loadAdminUsername()
  switchDeviceSource(true)
  loadDeviceConfigs()
}
const handleAdminLogout = () => {
  adminUsername.value = ''
  switchDeviceSource(false)
  // 退出管理员后若访客页面已被关闭，回到管理员登录页
  fetch('/api/register/status', { cache: 'no-store' })
    .then(r => r.json())
    .then(d => {
      if (d.guest_page_enabled === false) router.replace('/admin')
    })
    .catch(() => {})
}
const handleUserDevicesSync = (serverMacs) => {
  if (adminManaged.value || !Array.isArray(serverMacs)) return
  let changed = false
  serverMacs.forEach(mac => {
    const norm = mac.replace(/[:-]/g, '').toLowerCase()
    if (!deviceList.value.some(entry => entry.mac.toLowerCase() === norm)) {
      deviceList.value.push({ mac: norm, version: 'v1' })
      changed = true
    }
  })
  if (changed && deviceList.value.length === 1) {
    selectedMac.value = deviceList.value[0].mac
  }
}
const handleAdminDevicesUpdated = (event) => {
  if (!adminManaged.value) return
  const nextDevices = Array.isArray(event.detail?.devices)
    ? event.detail.devices
    : loadStoredDevices(STORAGE_ADMIN_DEVICES)
  const previousMacs = new Set(deviceList.value.map(entry => entry.mac.toLowerCase()))
  deviceList.value = nextDevices
  if (!deviceList.value.some(entry => entry.mac === selectedMac.value)) {
    selectedMac.value = deviceList.value[0]?.mac || null
  }
  nextDevices
    .filter(entry => !previousMacs.has(entry.mac.toLowerCase()))
    .forEach(entry => subscribeDevice(entry))
}

onMounted(async () => {
  // 访客开关校验：关闭且未登录管理员 → 跳管理员登录页（后端同时已封禁 MQTT 登录接口）
  try {
    const res = await fetch('/api/register/status', { cache: 'no-store' })
    const data = await res.json().catch(() => ({}))
    if (data.guest_page_enabled === false && !localStorage.getItem('mqtt_admin_token')) {
      router.replace('/admin')
      return
    }
  } catch (e) {
    console.warn('获取访客开关失败，按开启处理', e)
  }
  gateReady.value = true

  deviceList.value = loadStoredDevices(adminManaged.value ? STORAGE_ADMIN_DEVICES : STORAGE_DEVICES)
  selectedMac.value = deviceList.value[0]?.mac || null
  if (adminManaged.value) loadDeviceConfigs()
  window.addEventListener('mqtt-admin-login', handleAdminLogin)
  window.addEventListener('mqtt-admin-logout', handleAdminLogout)
  window.addEventListener('mqtt-admin-devices-updated', handleAdminDevicesUpdated)
  // 周期拉取状态兜底：连接期间若某条 state 事件被丢弃，1 分钟内自动纠正
  stateRefreshTimer = setInterval(() => {
    if (connected.value) {
      queryAllStates()
      autoCleanStaleCountdowns()
    }
  }, 60000)
})

onUnmounted(() => {
  if (stateRefreshTimer) clearInterval(stateRefreshTimer)
  window.removeEventListener('mqtt-admin-login', handleAdminLogin)
  window.removeEventListener('mqtt-admin-logout', handleAdminLogout)
  window.removeEventListener('mqtt-admin-devices-updated', handleAdminDevicesUpdated)
})

// Persist device list
watch(deviceList, (val) => {
  if (adminManaged.value) return
  localStorage.setItem(STORAGE_DEVICES, JSON.stringify(val))
}, { deep: true })

const connectMqtt = (config) => {
  if (client.value) {
    client.value.end()
  }

  connectionState.value = 'connecting'
  connectionError.value = ''

  const { host, username, password, clientId } = config
  console.log(`Connecting to ${host}...`)

  try {
    const mqttClient = mqtt.connect(host, {
      username,
      password,
      clientId,
      clean: true,
      connectTimeout: 4000,
    })

    mqttClient.on('connect', () => {
      console.log('MQTT Connected')
      connected.value = true
      connectionState.value = 'connected'
      connectionError.value = ''
      mqttReconnectTS = Date.now() // 记录重连时间，10s 窗口内首条 state 触发恢复
      recoveredDevices.clear()
      // Subscribe to all tracked devices
      deviceList.value.forEach(entry => subscribeDevice(entry))
      // 连接/重连后稍等片刻，让状态回读到达，再清理过期的倒计时任务
      setTimeout(autoCleanStaleCountdowns, 3000)
    })

    mqttClient.on('message', (topic, message) => {
      const msgStr = message.toString()
      // v2 HA-style topics
      if (topic.startsWith('homeassistant/')) {
        handleV2Message(topic, msgStr)
        return
      }
      // v1 JSON messages
      try {
        const payload = JSON.parse(msgStr)
        handleMessage(topic, payload)
      } catch (e) {
        console.warn('Failed to parse message', e)
      }
    })

    mqttClient.on('error', (err) => {
      console.error('MQTT Error', err)
      connected.value = false
      connectionState.value = 'error'
      connectionError.value = err?.message || 'MQTT 连接失败'
    })

    mqttClient.on('close', () => {
      console.log('MQTT Closed')
      connected.value = false
      if (connectionState.value !== 'error') {
        connectionState.value = 'disconnected'
      }
    })

    client.value = mqttClient
  } catch (e) {
    console.error('Connection factory error', e)
    connected.value = false
    connectionState.value = 'error'
    connectionError.value = e?.message || '无法创建 MQTT 连接'
  }

}

const disconnectMqtt = () => {
  if (client.value) {
    client.value.end()
    client.value = null
  }
  connected.value = false
  connectionState.value = 'disconnected'
  connectionError.value = ''
  recoveredDevices.clear() // 断连后重置，下次连接时允许恢复所有设备
}

const getDeviceVersion = (mac) => {
  // 管理员模式：服务端持久化的固件版本优先（跨浏览器/内外网一致，见 handoff 教训#3）
  if (adminManaged.value) {
    const fw = deviceConfigs[mac]?.firmware
    if (fw === 'v1' || fw === 'v2') return fw
  }
  const entry = deviceList.value.find(d => d.mac === mac)
  return entry?.version || 'v1'
}

// 切换设备固件版本：保存到服务端 + 更新本地 + 按新 topic 方案重新订阅
const changeDeviceFirmware = async (mac, version) => {
  if (version !== 'v1' && version !== 'v2') return
  const norm = mac.replace(/[:-]/g, '').toLowerCase()
  const oldVersion = getDeviceVersion(norm)
  if (oldVersion === version) return
  // 先更新本地即时生效
  if (!deviceConfigs[norm]) deviceConfigs[norm] = {}
  deviceConfigs[norm].firmware = version
  const entry = deviceList.value.find(d => d.mac === norm || d.mac.toLowerCase() === norm)
  if (entry) entry.version = version
  localStorage.setItem(STORAGE_ADMIN_DEVICES, JSON.stringify(deviceList.value))
  // 重新订阅: 退订旧 topic, 订阅新 topic
  if (client.value && connected.value) {
    unsubscribeDevice({ mac: norm, version: oldVersion })
    subscribeDevice({ mac: norm, version })
    if (version === 'v1') queryDeviceState(norm)
  }
  // 持久化到服务端
  try {
    await saveDeviceConfig({ mac: norm, firmware: version })
  } catch (e) {
    console.warn('保存固件版本失败', e)
    throw e
  }
}

// 按固件版本返回该设备应订阅的全部 topic（v1: device/ztc1/...; v2: homeassistant/...）
// v2 设备实际发布的是【大写 MAC】topic(抓包实测 homeassistant/switch/D0BAE4637BE5/...),
// 而管理员设备列表存的 mac 是 ACL 里的小写 —— MQTT topic 大小写敏感,
// 因此 v2 对大小写两种变体都订阅, 保证两种存储形态都能收到
const topicsForDevice = (mac, version) => {
  if (version === 'v2') {
    const upper = mac.toUpperCase()
    const lower = mac.toLowerCase()
    const variants = upper === lower ? [upper] : [upper, lower]
    const out = []
    for (const m of variants) {
      out.push(
        `homeassistant/switch/${m}/+/state`,
        `homeassistant/switch/${m}/+/config`,
        `homeassistant/sensor/${m}/+/state`,
        `homeassistant/sensor/${m}/+/config`,
        `homeassistant/button/${m}/+/config`,
      )
    }
    return out
  }
  return [
    `device/ztc1/${mac}/sensor`,
    `device/ztc1/${mac}/state`,
    `device/ztc1/${mac}/availability`,
  ]
}

const unsubscribeDevice = (entry) => {
  if (!client.value) return
  const { mac, version } = typeof entry === 'object' ? entry : { mac: entry, version: getDeviceVersion(entry) }
  client.value.unsubscribe(topicsForDevice(mac, version), (err) => {
    if (err) console.error(`Failed to unsubscribe ${mac}`, err)
  })
}

const subscribeDevice = (entry) => {
  if (!client.value || !connected.value) return
  const mac = typeof entry === 'object' ? entry.mac : entry
  // 服务端固件优先(getDeviceVersion 内含本地回退)
  const version = getDeviceVersion(mac)
  const isV2 = version === 'v2'
  client.value.subscribe(topicsForDevice(mac, version), (err) => {
    if (err) {
      console.error(`Failed to subscribe ${mac}`, err)
    } else {
      console.log(`Subscribed to ${mac} (${version})`)
      // v1: 订阅完成后主动拉取一次完整状态，避免断线重连后错过 state 事件导致状态冻结
      if (!isV2) queryDeviceState(mac)
    }
  })
}

// 查询设备当前状态：zControl v1 固件对 setting:{mqtt_uri:null} 会回显完整 state
const queryDeviceState = (mac) => {
  if (!client.value || !connected.value) return
  if (getDeviceVersion(mac) === 'v2') return
  client.value.publish(`device/ztc1/${mac}/set`, JSON.stringify({ mac, setting: { mqtt_uri: null } }))
}

const queryAllStates = () => {
  deviceList.value.forEach(entry => queryDeviceState(typeof entry === 'object' ? entry.mac : entry))
}

// 手动强制刷新所有设备插口状态（头部按钮）
const refreshToast = ref('')
let refreshToastTimer = null
const refreshAllStates = () => {
  if (!client.value || !connected.value) {
    refreshToast.value = '尚未连接 MQTT，无法刷新'
  } else {
    queryAllStates()
    autoCleanStaleCountdowns()
    refreshToast.value = '已请求刷新全部设备状态'
  }
  if (refreshToastTimer) clearTimeout(refreshToastTimer)
  refreshToastTimer = setTimeout(() => { refreshToast.value = '' }, 2500)
}

// 清理已到执行时间的一次性倒计时任务：固件执行一次后不会自动停用，
// 这里主动给设备下发 task_4 on=0，避免任务5一直停留在"启用"状态。
// 同时把停用结果同步到服务端配置，否则设备重连时 recoverDeviceAfterReconnect
// 会把服务端仍为 on=1 的 task_4 重新下发，导致过期倒计时被"复活"。
const autoCleanStaleCountdowns = () => {
  if (!client.value || !connected.value) return
  const now = Math.floor(Date.now() / 1000)
  deviceList.value.forEach((entry) => {
    const mac = typeof entry === 'object' ? entry.mac : entry
    let metas = {}
    try { metas = JSON.parse(localStorage.getItem(`ztc1_countdown_${mac}`) || '{}') } catch (e) { metas = {} }
    let changed = false
    Object.keys(metas).forEach((plugIdxStr) => {
      const plugIndex = parseInt(plugIdxStr, 10)
      const m = metas[plugIdxStr]
      if (!m || !m.target_ts || m.target_ts > now) return
      const task = devices[mac]?.[`plug_${plugIndex}`]?.setting?.task_4
      const taskData = {
        hour: task?.hour ?? 0,
        minute: task?.minute ?? 0,
        repeat: 0,
        action: task?.action ?? 1,
        on: 0
      }
      client.value.publish(`device/ztc1/${mac}/set`, JSON.stringify({
        mac,
        [`plug_${plugIndex}`]: { setting: { task_4: taskData } }
      }))
      // 同步服务端任务配置（管理员模式），防止重连恢复时重新启用
      const serverTasks = deviceConfigs[mac]?.tasks
      if (serverTasks?.[plugIdxStr]?.task_4) {
        serverTasks[plugIdxStr] = { ...serverTasks[plugIdxStr], task_4: { ...serverTasks[plugIdxStr].task_4, on: 0 } }
        saveDeviceConfig({ mac, tasks: { ...serverTasks }, tasksReplace: true })
          .catch(err => console.warn('同步倒计时停用状态失败', err))
      }
      delete metas[plugIdxStr]
      changed = true
    })
    if (changed) {
      localStorage.setItem(`ztc1_countdown_${mac}`, JSON.stringify(metas))
    }
  })
}

// ---- 设备重连恢复：自动同步时间 + 重新下发定时任务 ----
const recoveredDevices = new Set() // 本次连接已恢复过的设备，避免重复下发
let mqttReconnectTS = 0 // MQTT 重连时间戳：10s 窗口内收到的首条 state 触发恢复

// 恢复开关：默认开启；排查时可 localStorage.setItem('ztc1_recovery_disabled','1') 关闭
const recoveryEnabled = () => localStorage.getItem('ztc1_recovery_disabled') !== '1'

// 任务值类型清洗：TaskEditor 的 v-model 未带 .number 时 hour/minute 会是字符串,
// 原版 v1 固件对非数字类型的解析/比较不健壮, 统一转数字; 其余字段原样透传
const sanitizeTaskData = (taskData) => {
  if (!taskData || typeof taskData !== 'object') return taskData
  const out = { ...taskData }
  for (const k of ['hour', 'minute', 'repeat', 'action', 'on']) {
    if (k in out && typeof out[k] !== 'number') {
      const n = Number(out[k])
      out[k] = Number.isFinite(n) ? n : out[k]
    }
  }
  return out
}

// 恢复任务下发节奏：逐插口拆包, 每条间隔 150ms 错峰。
// v1 原版固件 MQTT 接收缓冲有限, 任务多时整包可超 2KB —— 超限消息被截断,
// 疑似触发原版固件看门狗复位(朋友端"每刷新一次排插重启一次", 他配了大量任务)。
// 拆包后单条与 TimerModal 手动保存同构(<300B), 且单条超限只损失一个插口。
const recoverDeviceAfterReconnect = (mac) => {
  if (!client.value || !connected.value) return
  if (getDeviceVersion(mac) === 'v2') return // v2 暂不处理
  if (!recoveryEnabled()) {
    console.log('[recovery] 已通过 ztc1_recovery_disabled 关闭, 跳过')
    return
  }
  if (recoveredDevices.has(mac)) return
  recoveredDevices.add(mac)

  console.log(`[recovery] 设备 ${mac} 重连，开始恢复时间和任务配置`)

  // 1. 同步设备时间（防时钟漂移导致定时任务触发时间偏移）
  const unix = Math.floor(Date.now() / 1000)
  client.value.publish(`device/ztc1/${mac}/set`, JSON.stringify({
    mac,
    setting: { time: unix }
  }))

  // 2. 从服务端保存的配置中恢复定时任务（逐插口拆包错峰下发）
  const cfg = deviceConfigs[mac]
  if (!cfg?.tasks) {
    console.log(`[recovery] 设备 ${mac} 无保存的任务配置，跳过`)
    return
  }
  const plugEntries = Object.entries(cfg.tasks)
  let delay = 300 // 等 time 消息先走
  plugEntries.forEach(([plugIdx, tasksMap]) => {
    const plugPayload = { mac, [`plug_${plugIdx}`]: { setting: {} } }
    const setting = plugPayload[`plug_${plugIdx}`].setting
    Object.entries(tasksMap).forEach(([taskKey, taskData]) => {
      setting[taskKey] = sanitizeTaskData(taskData)
    })
    const body = JSON.stringify(plugPayload)
    setTimeout(() => {
      if (!client.value || !connected.value) return
      client.value.publish(`device/ztc1/${mac}/set`, body)
      console.log(`[recovery] 下发 ${mac} 插口${plugIdx} 任务(${body.length}B)`)
    }, delay)
    delay += 150
  })
  if (plugEntries.length) {
    console.log(`[recovery] 设备 ${mac} 任务配置已排队: ${plugEntries.length} 个插口, 共 ${delay}ms 发完`)
  }
}

// 周期拉取状态兜底：连接期间若某条 state 事件被丢弃，1 分钟内自动纠正
let stateRefreshTimer = null

// 深合并：对嵌套对象递归合并，避免浅覆盖导致已保存的定时任务丢失
function isPlainObject(v) {
  return v !== null && typeof v === 'object' && !Array.isArray(v)
}

function deepMerge(target, source) {
  if (!isPlainObject(target) || !isPlainObject(source)) {
    return source
  }
  const out = { ...target }
  for (const key of Object.keys(source)) {
    const sv = source[key]
    if (isPlainObject(sv) && isPlainObject(out[key])) {
      out[key] = deepMerge(out[key], sv)
    } else {
      out[key] = sv
    }
  }
  return out
}

const handleMessage = (topic, payload) => {
    // If payload is a simpler value (like availability), handle it
    if (typeof payload !== 'object') {
       // Check if topic is availability
       if(topic.endsWith('/availability')) {
           const parts = topic.split('/')
           const mac = parts[2] // device/ztc1/MAC/availability
           if(!devices[mac]) devices[mac] = { mac }
           devices[mac].available = (payload == 1) // Loose equal as it might be number or string
           return
       }
    }

  // Determine MAC
  // Some payloads might not have MAC inside if they are defined by topic
  // But our spec says JSON payloads usually have it.
  // However, state topic from yaml example: value_template: '{{ value_json.plug_0.on }}'
  // implies payload is json. The `payload_on` includes mac.
  // Let's assume the device state reply includes mac as per original docs.
  
  let mac = payload.mac
  if (!mac) {
      // Try to extract from topic if payload doesn't have it
      const parts = topic.split('/')
      if(parts[0] === 'device' && parts[1] === 'ztc1' && parts[2]) {
          mac = parts[2]
      }
  }
  
  if (!mac) return

  // 检测重连触发条件：
  // 1. 设备之前标记为离线（available === false），现在收到状态数据
  // 2. MQTT 刚重连（10s 窗口内），该设备首条 state 到达时强制恢复
  const justReconnected = (Date.now() - mqttReconnectTS) < 10000
  const shouldRecover = (devices[mac] && devices[mac].available === false) || (justReconnected && !recoveredDevices.has(mac))

  if (!devices[mac]) {
    devices[mac] = { mac }
  }

  // 深合并状态，保留 device 中已存在的嵌套字段（如各插口的定时任务 setting）
  Object.assign(devices[mac], deepMerge(devices[mac], payload))

  // 设备重新上线：自动同步时间 + 恢复定时任务（延迟 1.5s 等设备就绪）
  if (shouldRecover && devices[mac].available !== false) {
    recoveredDevices.delete(mac) // 允许本次恢复
    setTimeout(() => recoverDeviceAfterReconnect(mac), 1500)
  }
}

const handleV2Message = (topic, msgStr) => {
  const parts = topic.split('/')
  if (parts.length < 5) return

  const [, type, rawMac, entity, suffix] = parts
  // v2 设备发布的是大写 MAC topic, 而设备列表(ACL来源)存的是小写 ——
  // 归一化回列表里的 mac, 否则状态会写进一个大小写不同的"幽灵设备"
  const norm = rawMac.toLowerCase()
  const known = deviceList.value.find(d => d.mac.toLowerCase() === norm)
  const mac = known ? known.mac : rawMac

  if (!devices[mac]) {
    devices[mac] = { mac, version: 'v2' }
  }
  if (!devices[mac].version) devices[mac].version = 'v2'
  
  if (suffix === 'config') {
    try {
      const config = JSON.parse(msgStr)
      if (config.device?.name) {
        devices[mac].deviceName = config.device.name
      }
      if (config.name) {
        if (!devices[mac].entityNames) devices[mac].entityNames = {}
        devices[mac].entityNames[entity] = config.name
      }
    } catch (e) {
      console.warn('Failed to parse v2 config', e)
    }
  } else if (suffix === 'state') {
    if (type === 'sensor') {
      try {
        const data = JSON.parse(msgStr)
        Object.assign(devices[mac], data)
      } catch (e) {
        console.warn('Failed to parse v2 sensor', e)
      }
    } else if (type === 'switch') {
      parseV2SwitchState(mac, msgStr)
    }
  }
}

// v2 命令回执表: key=`${mac}|${entity}|${state}` -> {timer, retries}
// 固件状态回显为 QoS0+retained, 总开关突发 7 条时 WiFi 上可能丢包,
// 且设备仅在状态变化时发布 => 丢一条 retained 就永久错误(实测发生过)。
// 对策: 用户命令先乐观更新 UI, 2.5s 内没等到设备对应回显则重发(幂等,
// 重发还会触发固件重发全量状态, 顺带修复 broker 里的错误 retained)。
const v2PendingAcks = new Map()
// 用户最近触碰过的单路插口 (mac|idx -> ts): total 回显禁止覆盖 8s 内的改动
const v2SocketTouch = {}
let v2SendSeq = 0 // v2 命令序号: 重发守卫(有更新命令时丢弃旧重发, 防止旧意图覆写新意图)

const touchV2Socket = (mac, idx) => { v2SocketTouch[`${mac}|${idx}`] = Date.now() }
const isV2SocketTouched = (mac, idx) => Date.now() - (v2SocketTouch[`${mac}|${idx}`] || 0) < 8000

const v2AckKeyOf = (payload) => {
  const parts = payload.trim().split(' ')
  if (parts[0] !== 'set' || parts.length < 4) return null
  let entity, state
  if (parts[1] === 'socket' && parts.length >= 5) {
    entity = `socket_${parseInt(parts[3])}`
    state = parseInt(parts[4])
  } else {
    entity = parts[1]
    state = parseInt(parts[3])
  }
  return `${parts[2].toUpperCase()}|${entity}|${state}`
}

const clearV2Ack = (key) => {
  const p = v2PendingAcks.get(key)
  if (p) { clearTimeout(p.timer); v2PendingAcks.delete(key) }
}

const armV2Ack = (mac, payload, seq, retries = 0) => {
  const key = v2AckKeyOf(payload)
  if (!key) return
  clearV2Ack(key)
  const timer = setTimeout(() => {
    if (!v2PendingAcks.has(key)) return
    v2PendingAcks.delete(key)
    if (seq !== v2SendSeq) {
      // 已有更新的命令: 重发旧命令会以旧意图覆写新意图(实测 bug), 丢弃
      console.log(`[v2ack] ${key} 回显超时, 但已有更新命令, 跳过重发`)
      return
    }
    if (retries < 2 && client.value && connected.value) {
      console.warn(`[v2ack] ${key} 回显超时, 重发第${retries + 1}次`)
      client.value.publish('device/ztc1/set', payload)
      armV2Ack(mac, payload, seq, retries + 1)
    }
  }, 2500)
  v2PendingAcks.set(key, { timer, retries })
}

const parseV2SwitchState = (mac, msgStr, fromEcho = true) => {
  const parts = msgStr.trim().split(' ')
  if (parts[0] !== 'set' || parts.length < 4) return

  const entity = parts[1]

  if (entity === 'socket' && parts.length >= 5) {
    const socketIndex = parseInt(parts[3])
    const state = parseInt(parts[4])
    devices[mac][`socket_${socketIndex}`] = state
  } else if (parts.length >= 4) {
    const state = parseInt(parts[3])
    devices[mac][entity] = state
    // 仅当"用户主动操作总开关"的乐观更新时联动 6 路。
    // 回显/重发到达的 total 严禁直接覆写单路: 否则 total 的 ack 重发会把
    // 用户刚做的单路修改踩掉(实测 bug: 点插口5关, 滞后的 total=1 回显
    // 触发全路覆写, UI 又变回全开, 看起来"控制不了")。
    // 8s 内被用户动过的单路保持用户意图, 其余路以 total 状态同步。
    if (entity === 'total_socket' && !fromEcho) {
      for (let i = 0; i < 6; i++) {
        if (!isV2SocketTouched(mac, i)) devices[mac][`socket_${i}`] = state
      }
    }
  }

  // 设备回显到达 => 对应回执完成(乐观更新不标记, 留给超时重发机制)
  if (fromEcho) {
    const key = v2AckKeyOf(msgStr)
    if (key) clearV2Ack(key)
  }
}

const addDevice = () => {
  if (adminManaged.value) {
    addDeviceError.value = '管理员设备由服务器 ACL 管理，不能在控制台手动添加'
    return
  }
  let mac = newMacInput.value.trim()
  addDeviceError.value = ''
  if (!mac) {
    addDeviceError.value = '请输入设备 MAC 地址'
    return
  }
  if (!/^[0-9a-fA-F]{12}$/.test(mac)) {
    addDeviceError.value = 'MAC 地址应为 12 位十六进制字符，例如 a1b2c3d4e5f6'
    return
  }
  mac = newDeviceVersion.value === 'v2' ? mac.toUpperCase() : mac.toLowerCase()

  if (deviceList.value.find(d => d.mac.toLowerCase() === mac.toLowerCase())) {
    addDeviceError.value = '该 MAC 地址已经添加，请勿通过切换 V1/V2 重复添加'
    return
  }

  const entry = { mac, version: newDeviceVersion.value }
  deviceList.value.push(entry)
  if (!devices[mac]) devices[mac] = { mac }
  devices[mac].version = newDeviceVersion.value
  subscribeDevice(entry)
  if (deviceList.value.length === 1) {
    selectedMac.value = mac
  }
  newMacInput.value = ''
}

const removeDevice = (mac) => {
  const idx = deviceList.value.findIndex(d => d.mac === mac)
  const entry = idx > -1 ? deviceList.value[idx] : null
  if (entry && client.value && connected.value) {
    unsubscribeDevice(entry)
  }
  if (idx > -1) {
    deviceList.value.splice(idx, 1)
  }
  delete devices[mac]
  if (selectedMac.value === mac) {
    selectedMac.value = deviceList.value[0]?.mac || null
  }
}

const requestRemoveDevice = (mac) => {
  if (adminManaged.value) return
  pendingRemoveMac.value = mac
}

const cancelRemoveDevice = () => {
  pendingRemoveMac.value = null
}

const confirmRemoveDevice = () => {
  if (!pendingRemoveMac.value) return
  removeDevice(pendingRemoveMac.value)
  pendingRemoveMac.value = null
}

const selectDevice = (mac) => {
  selectedMac.value = mac
}

const sendCommand = (cmd) => {
    if (!client.value || !connected.value) return
    let mac = cmd.mac
    const version = getDeviceVersion(mac)
    // v2 固件(源码 user_mqtt_client.c L173/385/796)只订阅固定 topic `device/ztc1/set`,
    // 命令文本里的 MAC 用大写(固件 %02X 生成 str_mac, strcmp 大小写敏感校验)
    const setTopic = version === 'v2' ? 'device/ztc1/set' : `device/ztc1/${mac}/set`
    if (version === 'v2' && cmd.payload) {
      const payload = cmd.payload.replaceAll(mac, mac.toUpperCase())
      // QoS0 突发易丢包且无自愈: 命令即意图, 先乐观更新 UI,
      // 2.5s 未收到设备回显则自动重发(幂等, 且触发固件重发全量状态)
      if (!devices[mac]) devices[mac] = { mac }
      parseV2SwitchState(mac, payload, false)
      const parts = payload.trim().split(' ')
      if (parts[1] === 'socket' && parts.length >= 5) {
        touchV2Socket(mac, parseInt(parts[3]))
      }
      const seq = ++v2SendSeq
      armV2Ack(mac, payload, seq)
      console.log('Sending v2', setTopic, payload)
      client.value.publish(setTopic, payload)
    } else {
      const payload = JSON.stringify({ ...cmd, mac })
      console.log('Sending', setTopic, payload)
      client.value.publish(setTopic, payload)
    }
}

const saveDeviceDisplayName = async ({ mac, displayName }) => {
  if (!adminManaged.value) return
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) throw new Error('管理员登录已失效')

  const response = await fetch(`/api/admin/device-metadata/${encodeURIComponent(mac)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify({ display_name: displayName })
  })
  const data = await response.json().catch(() => ({}))
  if (response.status === 401) {
    handleAdminUnauthorized()
  }
  if (!response.ok) throw new Error(data.error || '保存设备名称失败')

  const entry = deviceList.value.find(item => item.mac === mac)
  if (entry) entry.display_name = data.display_name || ''
  localStorage.setItem(STORAGE_ADMIN_DEVICES, JSON.stringify(deviceList.value))
}

// 从后端加载管理员账号下全部设备的配置（插口名/备注、布局、定时任务）
const loadDeviceConfigs = async () => {
  if (!adminManaged.value) return
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) return
  try {
    const response = await fetch('/api/admin/device-config', {
      headers: { Authorization: `Bearer ${token}` }
    })
    if (response.status === 401) {
      handleAdminUnauthorized()
      return
    }
    if (!response.ok) return
    const data = await response.json().catch(() => ({}))
    Object.keys(deviceConfigs).forEach(k => delete deviceConfigs[k])
    Object.entries(data || {}).forEach(([mac, cfg]) => {
      deviceConfigs[mac.toLowerCase()] = cfg || {}
    })
    // 按服务端固件回填本地设备列表版本，保证徽章/订阅与持久化配置一致
    let listChanged = false
    deviceList.value.forEach((entry) => {
      const fw = deviceConfigs[entry.mac.toLowerCase()]?.firmware
      if ((fw === 'v1' || fw === 'v2') && entry.version !== fw) {
        entry.version = fw
        listChanged = true
      }
    })
    if (listChanged) localStorage.setItem(STORAGE_ADMIN_DEVICES, JSON.stringify(deviceList.value))
  } catch (e) {
    console.warn('加载设备配置失败', e)
  }
}

// 保存单台设备配置到后端
const saveDeviceConfig = async ({ mac, layout, remarks, tasks, tasksReplace, firmware, version }) => {
  if (!adminManaged.value) return
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) throw new Error('管理员登录已失效')
  const norm = mac.replace(/[:-]/g, '').toLowerCase()
  const payload = {}
  if (layout !== undefined) payload.layout = layout
  if (remarks !== undefined) payload.remarks = remarks
  if (tasks !== undefined) {
    payload.tasks = tasks
    if (tasksReplace) payload.tasks_replace = true
  }
  if (firmware !== undefined) payload.firmware = firmware
  if (version !== undefined) payload.version = version
  if (Object.keys(payload).length === 0) return

  const response = await fetch(`/api/admin/device-config/${encodeURIComponent(norm)}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`
    },
    body: JSON.stringify(payload)
  })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    if (response.status === 401) {
      handleAdminUnauthorized()
      throw new Error('管理员登录已失效')
    }
    const msg = data.error || `保存设备配置失败 (HTTP ${response.status})`
    // 各调用点只 console.warn（为了不卡 UI），失败必须在这里显式提示，
    // 否则用户会以为定时任务已经存好了
    notifyAdminError(`保存到服务器失败：${msg}\n界面上的显示只是浏览器本地状态，刷新后会丢失。`)
    throw new Error(msg)
  }
  if (!deviceConfigs[norm]) deviceConfigs[norm] = {}
  if (payload.layout !== undefined) deviceConfigs[norm].layout = payload.layout
  if (payload.remarks !== undefined) deviceConfigs[norm].remarks = payload.remarks
  if (payload.tasks !== undefined) deviceConfigs[norm].tasks = payload.tasks
  if (payload.firmware !== undefined) deviceConfigs[norm].firmware = payload.firmware
  if (payload.version !== undefined) deviceConfigs[norm].version = payload.version
}

const selectedDeviceState = computed(() => {
  if (!selectedMac.value) return null
  const state = devices[selectedMac.value] || { mac: selectedMac.value }
  const entry = deviceList.value.find(d => d.mac === selectedMac.value)
  if (entry) {
    state.version = entry.version
    state.displayName = entry.display_name || ''
    state.adminManaged = adminManaged.value
  }
  return state
})

// 获取定时任务执行日志（推断型，管理员接口）
const fetchTaskLogs = async ({ mac, plug, limit }) => {
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) throw new Error('需要管理员登录后查看')
  const params = new URLSearchParams({ mac })
  if (plug !== undefined && plug >= 0) params.set('plug', String(plug))
  params.set('limit', String(limit || 500))
  const res = await fetch(`/api/admin/task-logs?${params.toString()}`, {
    headers: { Authorization: `Bearer ${token}` }
  })
  const data = await res.json().catch(() => ({}))
  if (res.status === 401) {
    handleAdminUnauthorized()
  }
  if (!res.ok) throw new Error(data.error || '获取任务日志失败')
  return data
}

const selectedDeviceHasData = computed(() => {
  if (!selectedMac.value) return false
  const state = devices[selectedMac.value]
  if (!state) return false
  return Object.keys(state).some(key => !['mac', 'version'].includes(key))
})

const serverDeviceConfig = computed(() => {
  if (!selectedMac.value) return {}
  return deviceConfigs[selectedMac.value] || {}
})

const deviceStatusText = (mac) => {
  const state = devices[mac]
  if (!state || Object.keys(state).every(key => ['mac', 'version'].includes(key))) {
    return '等待设备数据'
  }
  if (state.available === false) return '离线'
  return state.power ? `${state.power}W` : '在线'
}

const sidebarCollapsed = ref(false)
const toggleSidebar = () => {
    sidebarCollapsed.value = !sidebarCollapsed.value
}

const importExportRef = ref(null)
const openImportExport = () => {
    importExportRef.value?.open()
}

// Theme: 'auto' | 'light' | 'dark'
const theme = ref(localStorage.getItem('ztc1_theme') || 'auto')

const themeIcon = computed(() => {
  if (theme.value === 'dark') return '🌙'
  if (theme.value === 'light') return '☀️'
  return '🌗'
})

const themeTitle = computed(() => {
  if (theme.value === 'dark') return '深色模式 (点击切换)'
  if (theme.value === 'light') return '浅色模式 (点击切换)'
  return '跟随系统 (点击切换)'
})

const applyTheme = (t) => {
  const html = document.documentElement
  html.classList.remove('light', 'dark')
  if (t === 'light') html.classList.add('light')
  else if (t === 'dark') html.classList.add('dark')
  // 'auto' = no class, media query takes over
}

const cycleTheme = () => {
  const order = ['auto', 'dark', 'light']
  const idx = order.indexOf(theme.value)
  theme.value = order[(idx + 1) % 3]
  localStorage.setItem('ztc1_theme', theme.value)
  applyTheme(theme.value)
}

// Apply saved theme on load
applyTheme(theme.value)

</script>

<template>
  <div v-if="!gateReady" class="welcome gate-loading">
    <p>加载中...</p>
  </div>
  <div v-else class="container">
    <div class="header-bar">
        <h1 class="header-title">zTC1 管理终端</h1>
        <div class="header-actions">
          <button class="theme-btn" @click="cycleTheme" :title="themeTitle">{{ themeIcon }}</button>
          <button class="refresh-btn" @click="refreshAllStates" :title="'强制刷新全部设备插口状态'">🔄</button>
          <button class="settings-btn migrate-btn" @click="openImportExport" title="配置迁移">📦</button>
          <div class="header-auth">
            <template v-if="adminManaged">
              <router-link to="/admin" class="nav-link admin-entry-link" title="管理员设置">⚙️</router-link>
              <span class="admin-user-chip" :title="adminUsername || '管理员'">
                <span class="admin-user-icon">👤</span>
                <span class="admin-user-name">{{ adminUsername || '管理员' }}</span>
              </span>
              <button class="logout-btn" @click="logoutAdmin" title="退出管理员">登出</button>
            </template>
            <template v-else>
              <router-link to="/register" class="nav-link">注册</router-link>
              <router-link to="/admin" class="nav-link admin-entry-link" title="管理员设置">⚙️</router-link>
            </template>
          </div>
        </div>
    </div>

    <ImportExport ref="importExportRef" />

    <Transition name="refresh-toast">
      <div v-if="refreshToast" class="refresh-toast">{{ refreshToast }}</div>
    </Transition>

    <MqttConfig
      v-if="gateReady"
      :connected="connected"
      :connection-state="connectionState"
      :connection-error="connectionError"
      @connect="connectMqtt"
      @disconnect="disconnectMqtt"
      @devices-sync="handleUserDevicesSync"
    />

    <main class="main-content">
      <div class="sidebar" :class="{ collapsed: sidebarCollapsed }">
        <div class="sidebar-header">
           <span v-if="!sidebarCollapsed">设备列表</span>
           <button class="toggle-sidebar-btn" @click="toggleSidebar">
             {{ sidebarCollapsed ? '»' : '«' }}
           </button>
        </div>
        
        <div class="add-device" v-if="!sidebarCollapsed && !adminManaged">
          <label for="device-mac">添加设备</label>
          <input id="device-mac" v-model="newMacInput" placeholder="MAC 地址，例如 a1b2c3d4e5f6" @keyup.enter="addDevice">
          <div class="firmware-options" aria-label="固件版本">
            <button
              type="button"
              :class="{ active: newDeviceVersion === 'v1' }"
              @click="newDeviceVersion = 'v1'"
            >V1 固件</button>
            <button
              type="button"
              :class="{ active: newDeviceVersion === 'v2' }"
              @click="newDeviceVersion = 'v2'"
            >V2 固件</button>
          </div>
          <p class="firmware-help">固件版本必须与排插实际安装的版本一致。</p>
          <p v-if="addDeviceError" class="add-device-error">{{ addDeviceError }}</p>
          <button class="add-device-btn" @click="addDevice">添加设备</button>
        </div>
        
        <ul class="device-list" v-if="!sidebarCollapsed">
          <li 
            v-for="entry in deviceList" 
            :key="entry.mac"
            :class="{ active: entry.mac === selectedMac }"
            @click="selectDevice(entry.mac)"
          >
            <div class="device-item-row">
              <span class="device-name">
                {{ entry.display_name || devices[entry.mac]?.deviceName || devices[entry.mac]?.name || entry.mac }}
              </span>
              <span class="version-badge">{{ entry.version }}</span>
               <button v-if="!adminManaged" class="del-btn" @click.stop="requestRemoveDevice(entry.mac)" title="删除设备">×</button>
            </div>
            <div class="device-status">
               {{ deviceStatusText(entry.mac) }}
            </div>
          </li>
        </ul>
        <div v-else class="collapsed-list">
            <div 
                v-for="entry in deviceList" 
                :key="entry.mac"
                class="collapsed-item"
                :class="{ active: entry.mac === selectedMac }"
                @click="selectDevice(entry.mac)"
                :title="entry.display_name || devices[entry.mac]?.deviceName || devices[entry.mac]?.name || entry.mac"
            >
                {{ (entry.display_name || devices[entry.mac]?.deviceName || devices[entry.mac]?.name || entry.mac).slice(-2) }}
            </div>
        </div>
      </div>

      <div class="content-area">
        <!-- 选中即渲染(不再要求已有数据): 没数据时固件切换仍可用,
             否则"固件标错->订阅不到数据->面板不渲染->无法改固件"会死锁 -->
        <DeviceControl
          v-if="selectedDeviceState"
          :key="selectedMac"
          :device="selectedDeviceState"
          :save-display-name="saveDeviceDisplayName"
          :device-config="serverDeviceConfig"
          :save-device-config="saveDeviceConfig"
          :change-firmware="changeDeviceFirmware"
          :fetch-task-logs="fetchTaskLogs"
          @send-cmd="sendCommand"
        />
        <div v-else-if="deviceList.length === 0" class="empty-state">
          MQTT 已连接，请先添加设备
        </div>
        <div v-else class="empty-state">
          请选择设备
        </div>
      </div>
    </main>

    <div v-if="pendingRemoveMac" class="confirm-overlay" @click.self="cancelRemoveDevice">
      <div class="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="remove-device-title">
        <h2 id="remove-device-title">删除设备</h2>
        <p>确定从控制台移除设备 {{ pendingRemoveMac }} 吗？</p>
        <div class="confirm-actions">
          <button class="confirm-cancel" @click="cancelRemoveDevice">取消</button>
          <button class="confirm-delete" @click="confirmRemoveDevice">删除</button>
        </div>
      </div>
    </div>

    <footer class="app-footer">
      版本 {{ APP_VERSION }}
    </footer>
  </div>
</template>

<style>
/* Global Styles */
*, *::before, *::after {
  box-sizing: border-box;
}

body {
  margin: 0;
  font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
  background-color: var(--bg-body);
  color: var(--text-primary);
}
.container {
  max-width: 1200px;
  margin: 0 auto;
  padding: 20px;
  overflow-x: hidden; /* Prevent page from being stretched */
}
.app-footer {
  margin-top: 30px;
  padding-top: 12px;
  border-top: 1px solid var(--border-light, #e5e5e5);
  text-align: center;
  font-size: 0.8em;
  color: var(--text-muted, #888);
}
.header-bar {
    position: relative;
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 20px;
    flex-wrap: wrap;
    gap: 10px;
}
.header-title {
    margin: 0;
    flex: 0 0 auto;
    text-align: left;
}
.header-actions {
    display: flex;
    gap: 4px;
    align-items: center;
    margin-left: auto;
    z-index: 1;
}
.header-auth {
    display: flex;
    align-items: center;
    gap: 2px;
    margin-left: 4px;
    padding-left: 4px;
    border-left: 1px solid var(--border-light, #e0e0e0);
}
/* 压过后面的 .nav-link（否则齿轮被 padding/灰底撑开，夜间模式尤其明显） */
.nav-link.admin-entry-link {
    font-size: 1.35em;
    text-decoration: none;
    line-height: 1;
    padding: 0;
    margin: 0;
    background: transparent;
    border-radius: 4px;
    opacity: 0.95;
}
.nav-link.admin-entry-link:hover {
    opacity: 1;
    background: var(--bg-hover);
}
.admin-user-chip {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    max-width: 140px;
    padding: 2px 8px;
    border-radius: 999px;
    background: transparent;
    color: var(--text-primary);
    font-size: 0.85em;
}
.admin-user-name {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.logout-btn {
    background: transparent;
    border: 1px solid var(--border-color, #ccc);
    border-radius: 6px;
    padding: 2px 10px;
    cursor: pointer;
    color: var(--text-secondary, #666);
    font-size: 0.85em;
}
.logout-btn:hover {
    color: #ef9a9a;
    border-color: #c62828;
    background: rgba(198, 40, 40, 0.08);
}

/* Mobile: header wraps to separate line */
@media (max-width: 600px) {
  .header-bar {
    flex-direction: column;
    align-items: stretch;
  }
  .header-bar h1 {
    order: 1;
    margin-bottom: 10px;
    text-align: left;
  }
  .header-actions {
    order: 2;
    justify-content: flex-end;
    flex-wrap: wrap;
  }
  .header-auth {
    border-left: none;
    padding-left: 0;
    margin-left: 4px;
  }
}
.settings-btn, .theme-btn {
    font-size: 1.5em;
    background: none;
    border: none;
    cursor: pointer;
    padding: 5px;
    color: var(--text-primary);
    line-height: 1;
    opacity: 0.8;
}
.settings-btn:hover, .theme-btn:hover {
    opacity: 1;
}
.refresh-btn {
    font-size: 1.3em;
    background: none;
    border: none;
    cursor: pointer;
    padding: 5px;
    color: var(--text-primary);
    line-height: 1;
    opacity: 0.8;
}
.refresh-btn:hover {
    opacity: 1;
    transform: rotate(45deg);
    transition: transform 0.3s;
}
.refresh-toast {
    position: fixed;
    top: 20px;
    left: 50%;
    transform: translateX(-50%);
    background: #323232;
    color: #fff;
    padding: 10px 24px;
    border-radius: 8px;
    font-size: 0.95em;
    z-index: 10000;
    box-shadow: 0 4px 12px rgba(0,0,0,0.25);
    pointer-events: none;
}
.refresh-toast-enter-active,
.refresh-toast-leave-active {
    transition: opacity 0.3s ease, transform 0.3s ease;
}
.refresh-toast-enter-from,
.refresh-toast-leave-to {
    opacity: 0;
    transform: translateX(-50%) translateY(-10px);
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
h1 {
  text-align: center;
  color: var(--text-heading);
  margin: 0;
}

.main-content {
  display: flex;
  gap: 20px;
  min-height: 500px;
}

.sidebar {
  width: 300px;
  background: var(--bg-card);
  border-radius: 8px;
  padding: 15px;
  box-shadow: 0 2px 4px var(--shadow-color);
  display: flex;
  flex-direction: column;
  transition: all 0.3s ease;
  overflow: hidden;
  height: fit-content;
  max-height: 80vh;
}

.sidebar.collapsed {
    width: 50px;
    padding: 15px 5px;
}

/* Mobile Adaptation */
@media (max-width: 768px) {
  .container {
    padding: 10px;
  }
  
  .main-content {
    flex-direction: column;
  }
  
  .sidebar {
    width: 100%;
    order: -1; /* Sidebar on top */
    max-height: 300px; /* Limit height on mobile */
  }
  
  .sidebar.collapsed {
    width: 100%;
    height: 50px;
    padding: 10px;
    flex-direction: row;
    align-items: center;
    overflow-x: auto;
    overflow-y: hidden;
  }
  
  .sidebar.collapsed .sidebar-header {
      margin-bottom: 0;
      margin-right: 10px;
  }
  
  .collapsed-list {
      flex-direction: row;
      flex-wrap: nowrap;
      overflow-x: auto;
      width: 100%;
      padding-bottom: 5px; /* Scrollbar space */
  }
  
  .collapsed-item {
      flex-shrink: 0;
  }
}

.sidebar-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 15px;
    min-height: 24px;
}
.toggle-sidebar-btn {
    background: none;
    border: none;
    cursor: pointer;
    font-size: 1.2em;
    padding: 0 5px;
    margin-left: auto;
    color: var(--text-primary);
}
.sidebar.collapsed .toggle-sidebar-btn {
    margin: 0 auto;
}
.sidebar-header span {
    font-weight: bold;
}

.collapsed-list {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: center;
}
.collapsed-item {
    width: 40px;
    height: 40px;
    border-radius: 50%;
    background: var(--bg-secondary);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.8em;
    cursor: pointer;
    border: 2px solid transparent;
}
.collapsed-item.active {
    background: var(--bg-active);
    border-color: #2196f3;
}

.content-area {
  flex: 1;
  background: var(--bg-card);
  border-radius: 8px;
  padding: 20px;
  box-shadow: 0 2px 4px var(--shadow-color);
}

.add-device {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 15px;
  min-width: 0; /* Prevent overflow */
}
.add-device label {
  font-weight: 600;
}
.add-device input {
  min-width: 0; /* Allow input to shrink */
  padding: 8px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-input);
  color: var(--text-primary);
}
.add-device .add-device-btn {
  padding: 8px 15px;
  background: #4caf50;
  color: white;
  border: none;
  border-radius: 4px;
  cursor: pointer;
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
.firmware-help,
.add-device-error {
  margin: 0;
  font-size: 0.8em;
}
.firmware-help {
  color: var(--text-muted);
}
.add-device-error {
  color: #f44336;
}
.version-badge {
  font-size: 0.7em;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  margin-left: 4px;
  flex-shrink: 0;
}

.device-list {
  list-style: none;
  padding: 0;
  margin: 0;
  overflow-y: auto;
  overflow-x: hidden; /* Prevent horizontal scroll */
  flex: 1; /* Take remaining space */
  min-height: 0; /* Allow shrinking below content size */
}
.device-list li {
  padding: 10px;
  border-bottom: 1px solid var(--border-light);
  cursor: pointer;
  transition: background 0.2s;
}
.device-list li:hover {
  background: var(--bg-hover);
}
.device-list li.active {
  background: var(--bg-active);
  border-left: 3px solid #2196f3;
}

.device-item-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  min-width: 0; /* Allow flex item to shrink below content size */
}
.device-name {
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
  min-width: 0; /* Allow text truncation to work */
}
.device-status {
  font-size: 0.8em;
  color: var(--text-muted);
  margin-top: 4px;
}

.del-btn {
  background: none;
  border: none;
  color: var(--text-muted);
  cursor: pointer;
  font-size: 1.2em;
  padding: 0 5px;
}
.del-btn:hover {
  color: #f44336;
}

.confirm-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: var(--overlay-bg);
}
.confirm-dialog {
  width: min(100%, 400px);
  padding: 20px;
  border-radius: 8px;
  background: var(--bg-card);
  box-shadow: 0 8px 24px var(--shadow-color);
}
.confirm-dialog h2 {
  margin: 0 0 10px;
  font-size: 1.2em;
}
.confirm-dialog p {
  margin: 0 0 20px;
  color: var(--text-secondary);
  overflow-wrap: anywhere;
}
.confirm-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.confirm-actions button {
  padding: 8px 16px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  cursor: pointer;
}
.confirm-cancel {
  background: var(--bg-secondary);
  color: var(--text-primary);
}
.confirm-delete {
  border-color: #d32f2f !important;
  background: #d32f2f;
  color: white;
}

.empty-state {
  display: flex;
  justify-content: center;
  align-items: center;
  height: 100%;
  color: var(--text-muted);
  font-size: 1.2em;
}
.welcome {
  text-align: center;
  margin-top: 50px;
  color: var(--text-secondary);
}
</style>
