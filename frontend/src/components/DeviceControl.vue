<script setup>
import { ref, computed, reactive, onMounted, onUnmounted, watch } from 'vue'
import TimerModal from './TimerModal.vue'
import PowerChart from './PowerChart.vue'

const props = defineProps({
  device: {
    type: Object,
    required: true
  },
  saveDisplayName: {
    type: Function,
    default: null
  },
  deviceConfig: {
    type: Object,
    default: () => ({})
  },
  saveDeviceConfig: {
    type: Function,
    default: null
  },
  changeFirmware: {
    type: Function,
    default: null
  },
  fetchTaskLogs: {
    type: Function,
    default: null
  }
})

const emit = defineEmits(['send-cmd', 'update-remark'])

const isV2 = computed(() => props.device.version === 'v2')

// 管理员可切换固件版本(v1/v2), 服务端持久化
const firmwareSaving = ref(false)
const canChangeFirmware = computed(() =>
  props.device.adminManaged && typeof props.changeFirmware === 'function'
)
// 是否已收到该设备的任何上报数据(用于"无数据"提示)
const hasDeviceData = computed(() =>
  Object.keys(props.device).some(k => !['mac', 'version', 'displayName', 'adminManaged'].includes(k))
)
const applyFirmware = async (ver) => {
  if (ver === props.device.version || firmwareSaving.value) return
  if (!confirm(`确定将该设备固件版本改为 ${ver.toUpperCase()} 吗？\n\n将按 ${ver} 的 topic 方案重新订阅。`)) return
  firmwareSaving.value = true
  try {
    await props.changeFirmware(props.device.mac, ver)
  } catch (e) {
    alert(e?.message || '切换固件版本失败')
  } finally {
    firmwareSaving.value = false
  }
}

// Helper to check if plug exists
const getPlug = (index) => props.device[`plug_${index}`]

const togglePlug = (index) => {
  if (isV2.value) {
    const current = props.device[`socket_${index}`]
    const newState = current === 1 ? 0 : 1
    const socketName = getV2SocketName(index)
    const action = newState === 1 ? '通电' : '断电'
    if (!confirm(`确定要让“${socketName}”${action}吗？\n\n请确认没有点错设备或插口。`)) return
    emit('send-cmd', {
      mac: props.device.mac,
      payload: `set socket ${props.device.mac} ${index} ${newState}`
    })
    return
  }
  const plug = getPlug(index)
  if (!plug) return
  const newState = plug.on === 1 ? 0 : 1
  const plugName = plug.name || `插口 ${index + 1}`
  const action = newState === 1 ? '通电' : '断电'
  if (!confirm(`确定要让“${plugName}”${action}吗？\n\n请确认没有点错设备或插口。`)) return
  
  const cmd = {
    mac: props.device.mac,
    [`plug_${index}`]: {
      on: newState
    }
  }
  emit('send-cmd', cmd)
}

const updatePlugName = (index, newName) => {
  const cmd = {
    mac: props.device.mac,
    [`plug_${index}`]: {
      setting: {
        name: newName
      }
    }
  }
  emit('send-cmd', cmd)
}

// Local rename state
const editingPlug = ref(-1)
const tempName = ref('')
const remarks = reactive({}) // Local remarks storage
const tasksCache = reactive({}) // { plugIndex: { task_0: {...}, ... } } 定时任务配置缓存（跨端同步）

// Load remarks from localStorage, then overlay with server config remarks
const loadRemarks = () => {
  // Clear previous remarks
  Object.keys(remarks).forEach(key => delete remarks[key])
  
  const saved = localStorage.getItem(`ztc1_remarks_${props.device.mac}`)
  if (saved) {
    try {
      Object.assign(remarks, JSON.parse(saved))
    } catch (e) { /* ignore */ }
  }
  // Server-side remarks (跨端同步) 覆盖本地
  if (props.deviceConfig?.remarks) {
    Object.assign(remarks, props.deviceConfig.remarks)
  }
}

// Watch for device changes to reload remarks
watch(() => props.device.mac, () => {
  loadRemarks()
  editingPlug.value = -1 // Reset editing state when switching devices
})

// 服务端配置异步加载完成后，刷新备注显示与定时任务缓存
watch(() => props.deviceConfig, (cfg) => {
  loadRemarks()
  syncTasksFromServer(cfg)
}, { deep: true })

// 用服务端定时任务配置初始化/更新本机缓存
const syncTasksFromServer = (cfg) => {
  const serverTasks = cfg?.tasks
  if (!serverTasks || typeof serverTasks !== 'object') return
  Object.keys(serverTasks).forEach(plugIdx => {
    if (!tasksCache[plugIdx]) tasksCache[plugIdx] = {}
    Object.assign(tasksCache[plugIdx], serverTasks[plugIdx] || {})
  })
}

onMounted(() => {
  loadRemarks()
  syncTasksFromServer(props.deviceConfig)

  if (!isV2.value) {
    // Query status for locks (v1 only)
    emit('send-cmd', {
        mac: props.device.mac,
        child_lock: null,
        led_lock: null
    })
  }
})

const startEdit = (index) => {
  if (isV2.value) {
    editingPlug.value = index
    tempName.value = remarks[index] || getV2SocketName(index)
    return
  }
  const plug = getPlug(index)
  if (!plug) return
  editingPlug.value = index
  // Prefer local remark, then device name
  tempName.value = remarks[index] || plug.name
}

const saveV2Edit = (index) => {
  if (editingPlug.value === index) {
    remarks[index] = tempName.value
    localStorage.setItem(`ztc1_remarks_${props.device.mac}`, JSON.stringify(remarks))
    persistRemarks()
    editingPlug.value = -1
  }
}

const saveEdit = (index) => {
  if (editingPlug.value === index) {
    // 1. Send name to device
    updatePlugName(index, tempName.value)
    
    // 2. Save locally
    remarks[index] = tempName.value
    localStorage.setItem(`ztc1_remarks_${props.device.mac}`, JSON.stringify(remarks))
    persistRemarks()
    
    editingPlug.value = -1
  }
}

// 将插口备注同步到后端（跨端一致）
const persistRemarks = () => {
  if (props.saveDeviceConfig) {
    try {
      props.saveDeviceConfig({ mac: props.device.mac, remarks: { ...remarks } })
        .catch(err => console.warn('保存插口备注到后端失败', err))
    } catch (e) {
      console.warn('保存插口备注到后端失败', e)
    }
  }
}

// Timer Modal
const timerPlug = ref(-1)

const openTimer = (index) => {
  timerPlug.value = index
}

const closeTimer = () => {
  timerPlug.value = -1
}

const updateTask = (plugIndex, taskIndex, taskData) => {
  // v1: 下发固件执行; v2: 固件无定时命令路径, 由后端调度器读配置代为执行,
  // 这里只更新本地缓存并同步服务端(调度器 15s 内拾取)
  if (!isV2.value) {
    const cmd = {
      mac: props.device.mac,
      [`plug_${plugIndex}`]: {
        setting: {
          [`task_${taskIndex}`]: taskData
        }
      }
    }
    emit('send-cmd', cmd)
  }

  // 更新本地缓存并同步到后端
  if (!tasksCache[plugIndex]) tasksCache[plugIndex] = {}
  tasksCache[plugIndex][`task_${taskIndex}`] = taskData
  persistTasks()
}

// ---- 倒计时（占用任务5/task_4）----
const countdownMetas = reactive({}) // { plugIndex: { target_ts, action } }

const loadCountdownMetas = () => {
  Object.keys(countdownMetas).forEach(k => delete countdownMetas[k])
  try {
    const saved = JSON.parse(localStorage.getItem(`ztc1_countdown_${props.device.mac}`) || '{}')
    Object.assign(countdownMetas, saved)
  } catch (e) { /* ignore */ }
}

const persistCountdownMetas = () => {
  const out = {}
  Object.keys(countdownMetas).forEach(k => { out[k] = countdownMetas[k] })
  localStorage.setItem(`ztc1_countdown_${props.device.mac}`, JSON.stringify(out))
}

watch(() => props.device.mac, () => loadCountdownMetas(), { immediate: true })

// 给 TimerModal 用的响应式快照
const countdownMetaForPlug = computed(() => {
  const m = countdownMetas[timerPlug.value]
  if (!m || !m.target_ts) return null
  return m
})

// 保存倒计时：先同步设备时间（防漂移），再把目标时刻写入 task_4
const saveCountdown = async (hours, minutes, action) => {
  const plugIndex = timerPlug.value
  if (plugIndex < 0) return

  // 1. 同步设备时间（v1 固件时钟漂移会直接影响触发精度; v2 由后端调度器执行, 无需）
  if (!isV2.value) {
    emit('send-cmd', {
      mac: props.device.mac,
      setting: { time: Math.floor(Date.now() / 1000) }
    })
  }

  // 2. 计算目标时刻并写入 task_4（一次性任务，直接覆盖）
  const target = new Date(Date.now() + (hours * 3600 + minutes * 60) * 1000)
  const taskData = {
    hour: target.getHours(),
    minute: target.getMinutes(),
    repeat: 0,
    action,
    on: 1
  }
  updateTask(plugIndex, 4, taskData)

  // 3. 记录元数据用于界面倒计时显示（仅显示用，执行仍由固件负责）
  countdownMetas[plugIndex] = {
    target_ts: Math.floor(target.getTime() / 1000),
    action
  }
  persistCountdownMetas()
}

// 停止倒计时：停用 task_4（保留任务字段，on:0）
const stopCountdown = () => {
  const plugIndex = timerPlug.value
  if (plugIndex < 0) return
  const existing = getTask(plugIndex, 4)
  const taskData = existing ? { ...existing, on: 0 } : { hour: 0, minute: 0, repeat: 0, action: 1, on: 0 }
  updateTask(plugIndex, 4, taskData)
  delete countdownMetas[plugIndex]
  persistCountdownMetas()
}

const getTask = (plugIndex, taskIndex) => {
  const plug = getPlug(plugIndex)
  const deviceTask = plug?.setting?.[`task_${taskIndex}`]
  if (deviceTask) return deviceTask
  return tasksCache[plugIndex]?.[`task_${taskIndex}`] || null
}

// 插口当前显示名（本地备注优先）
const getPlugDisplayName = (index) => {
  return remarks[index] || getPlug(index)?.name || `插口 ${index + 1}`
}

// 已启用的定时任务数量
const getEnabledTaskCount = (plugIndex) => {
  let c = 0
  for (let i = 0; i < 5; i++) {
    const t = getTask(plugIndex, i)
    if (t && t.on === 1) c++
  }
  return c
}

// 将定时任务配置同步到后端（跨端一致，任务执行仍由设备固件负责）
// tasksCache 始终是该设备的完整任务表，用全量替换语义保存，
// 从任务编辑器里删除的任务才能真正从服务端消失（v1.9.0）
const persistTasks = () => {
  if (props.saveDeviceConfig) {
    try {
      props.saveDeviceConfig({ mac: props.device.mac, tasks: { ...tasksCache }, tasksReplace: true })
        .catch(err => console.warn('保存定时任务到后端失败', err))
    } catch (e) {
      console.warn('保存定时任务到后端失败', e)
    }
  }
}

// 删除任务：v1 下发 on=0 停用 + 从配置移除; v2 固件无任务命令路径, 仅改配置
// (停用即等效删除；删除后跨端回显、重连恢复与后端调度器都不会再出现该任务)
const deleteTask = (plugIndex, taskIndex) => {
  const taskKey = `task_${taskIndex}`
  const existing = getTask(plugIndex, taskIndex)
  if (existing && !isV2.value) {
    emit('send-cmd', {
      mac: props.device.mac,
      [`plug_${plugIndex}`]: {
        setting: { [taskKey]: { ...existing, on: 0 } }
      }
    })
  }
  if (tasksCache[plugIndex]) {
    delete tasksCache[plugIndex][taskKey]
    if (Object.keys(tasksCache[plugIndex]).length === 0) delete tasksCache[plugIndex]
  }
  if (taskIndex === 4 && countdownMetas[plugIndex] !== undefined) {
    delete countdownMetas[plugIndex]
    persistCountdownMetas()
  }
  persistTasks()
}

const bitToDays = (bits) => {
  if (bits === 0) return '一次'
  if (bits === 127) return '每天'
  const days = ['一','二','三','四','五','六','日']
  let res = []
  for(let i=0; i<7; i++) {
    if ((bits >> i) & 1) res.push(days[i])
  }
  return '周' + res.join('')
}

// Advanced Settings
const deviceSettings = reactive({
    mqtt_uri: '',
    mqtt_port: 1883,
    mqtt_user: '',
    mqtt_password: '',
    ota: ''
})

watch(() => props.device.setting, (val) => {
    if(val) {
        // Only update if not currently editing? Or just sync.
        // Simple sync
        if(val.mqtt_uri !== undefined) deviceSettings.mqtt_uri = val.mqtt_uri
        if(val.mqtt_port !== undefined) deviceSettings.mqtt_port = val.mqtt_port
        if(val.mqtt_user !== undefined) deviceSettings.mqtt_user = val.mqtt_user
        // password might be hidden or not returned
        if(val.ota !== undefined) deviceSettings.ota = val.ota
    }
}, { deep: true, immediate: true })

const querySettings = () => {
    emit('send-cmd', { mac: props.device.mac, setting: { mqtt_uri: null } })
}

const saveMqttSettings = () => {
   emit('send-cmd', { 
       mac: props.device.mac, 
       setting: { 
           mqtt_uri: deviceSettings.mqtt_uri,
           mqtt_port: Number(deviceSettings.mqtt_port),
           mqtt_user: deviceSettings.mqtt_user,
           mqtt_password: deviceSettings.mqtt_password
       } 
   })
}

const performOta = () => {
   if(!deviceSettings.ota) return
   // Ensure URL format is correct per docs (no @, no redirect)
   emit('send-cmd', { mac: props.device.mac, setting: { ota: deviceSettings.ota } })
}


const formatPower = computed(() => {
  if (isV2.value) {
    return props.device.power || '0.0'
  }
  return props.device.power || '0.0'
})

const monthlyKwh = computed(() => {
  const power = parseFloat(props.device.power) || 0
  return (power * 24 * 30 / 1000).toFixed(1)
})

const formatTime = (seconds) => {
  if (!seconds) return '0s'
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = seconds % 60
  return `${h}h ${m}m ${s}s`
}

// Device Name Editing
const deviceNameEditing = ref(false)
const tempDeviceName = ref('')

const startDeviceEdit = () => {
  tempDeviceName.value = props.device.displayName || props.device.name || props.device.mac
  deviceNameEditing.value = true
}

const saveDeviceName = async () => {
  const displayName = tempDeviceName.value.trim()
  if (props.device.adminManaged) {
    try {
      if (!props.saveDisplayName) throw new Error('设备名称保存功能不可用')
      await props.saveDisplayName({ mac: props.device.mac, displayName })
      deviceNameEditing.value = false
    } catch (error) {
      alert(error?.message || '保存设备名称失败')
    }
    return
  }
  const cmd = {
    mac: props.device.mac,
    setting: {
      name: tempDeviceName.value
    }
  }
  emit('send-cmd', cmd)
  deviceNameEditing.value = false
}

const toggleChildLock = () => {
    if (isV2.value) {
      const current = props.device.childLock === 1 ? 0 : 1
      emit('send-cmd', {
        mac: props.device.mac,
        payload: `set childLock ${props.device.mac} ${current}`
      })
      return
    }
    emit('send-cmd', {
        mac: props.device.mac,
        child_lock: props.device.child_lock === 1 ? 0 : 1
    })
}

const toggleLedLock = () => {
    if (isV2.value) {
      const current = props.device.led === 1 ? 0 : 1
      emit('send-cmd', {
        mac: props.device.mac,
        payload: `set led ${props.device.mac} ${current}`
      })
      return
    }
    emit('send-cmd', {
        mac: props.device.mac,
        led_lock: props.device.led_lock === 1 ? 0 : 1
    })
}

// ---- 设备时间同步 (v1 固件) ----
const timeSyncState = ref('') // '' | 'sending' | 'ok' | 'fail'
const timeSyncMsg = ref('')

const syncDeviceTime = () => {
  if (isV2.value) return
  const unix = Math.floor(Date.now() / 1000)
  timeSyncState.value = 'sending'
  timeSyncMsg.value = '正在同步设备时间...'
  // v1 固件通过 setting.time 接受 Unix 时间戳
  emit('send-cmd', {
    mac: props.device.mac,
    setting: { time: unix }
  })
  // 因设备不回显确认，仅提示已发送（发送走 MQTT，成功率极高）
  setTimeout(() => {
    timeSyncState.value = 'ok'
    const now = new Date()
    const pad = n => String(n).padStart(2, '0')
    timeSyncMsg.value = `已发送时间同步 ${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}（设备时钟须与本地一致才会按时执行定时任务）`
  }, 1500)
}

// v2: total socket toggle
const toggleTotalSocket = () => {
    const current = props.device.total_socket === 1 ? 0 : 1
    const action = current === 1 ? '全部通电' : '全部断电'
    const deviceName = props.device.name || props.device.mac
    if (!confirm(`确定要让设备“${deviceName}”${action}吗？\n\n此操作会同时影响所有插口，请确认没有点错设备。`)) return
    emit('send-cmd', {
      mac: props.device.mac,
      payload: `set total_socket ${props.device.mac} ${current}`
    })
}

// v2: reboot
const rebootDevice = () => {
    if (!confirm('确定要重启设备吗？')) return
    emit('send-cmd', {
      mac: props.device.mac,
      payload: `reboot ${props.device.mac}`
    })
}

// v2: soft reboot
const softRebootDevice = () => {
    if (!confirm('确定要软重启设备吗？')) return
    emit('send-cmd', {
      mac: props.device.mac,
      payload: `soft_reboot ${props.device.mac}`
    })
}

// v2 helpers
const getV2SocketState = (index) => {
  return props.device[`socket_${index}`]
}

const getV2SocketName = (index) => {
  return props.device.entityNames?.[`socket_${index}`] || `插口 ${index + 1}`
}

// Layout Control
// persistedLayout = 用户选择并保存到服务端的布局；gridLayout = 实际显示布局
// （移动端 1x6 一行六列体验差，仅在显示层降级为 auto，不写回服务端，
//   避免手机访问一次就把桌面端选的 1x6 覆盖掉）
const isMobile = ref(window.innerWidth < 768)
const persistedLayout = ref('1x6')
const gridLayout = computed(() =>
  isMobile.value && persistedLayout.value === '1x6' ? 'auto' : persistedLayout.value
)

const handleResize = () => {
  isMobile.value = window.innerWidth < 768
}

onMounted(() => {
  window.addEventListener('resize', handleResize)
  handleResize()
  // 组件挂载时服务端配置可能已加载完成（watch 不会触发），补一次初始化
  if (props.deviceConfig?.layout) persistedLayout.value = props.deviceConfig.layout
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
})

// 服务端布局异步到达时同步（只更新持久化值，不触发回写）
watch(() => props.deviceConfig?.layout, (val) => {
  if (val && val !== persistedLayout.value) persistedLayout.value = val
})

// 用户主动改布局时保存到后端
watch(persistedLayout, (val) => {
  if (props.saveDeviceConfig) {
    try {
      props.saveDeviceConfig({ mac: props.device.mac, layout: val })
        .catch(err => console.warn('保存布局到后端失败', err))
    } catch (e) {
      console.warn('保存布局到后端失败', e)
    }
  }
})

const gridStyle = computed(() => {
    switch(gridLayout.value) {
        case '1x6': return { gridTemplateColumns: 'repeat(6, 1fr)' }
        case '2x3': return { gridTemplateColumns: 'repeat(3, 1fr)' }
        case '3x2': return { gridTemplateColumns: 'repeat(2, 1fr)' }
        default: return { gridTemplateColumns: 'repeat(auto-fill, minmax(140px, 1fr))' }
    }
})

// ---- 定时任务执行日志（推断型，仅供参考）----
const taskLogOpen = ref(false)
const taskLogs = ref([])
const taskLogPlug = ref(-1) // -1 = 全部插口
const taskLogLoading = ref(false)
const taskLogError = ref('')
const taskLogLoadedFor = ref('') // mac+plug 组合，切换后重新加载

const loadTaskLogs = async () => {
  if (!props.fetchTaskLogs || !props.device.mac) return
  taskLogLoading.value = true
  taskLogError.value = ''
  try {
    const data = await props.fetchTaskLogs({
      mac: props.device.mac,
      plug: taskLogPlug.value,
      limit: 500
    })
    taskLogs.value = Array.isArray(data.logs) ? data.logs : []
    taskLogLoadedFor.value = `${props.device.mac}:${taskLogPlug.value}`
  } catch (e) {
    taskLogError.value = e?.message || '获取任务日志失败'
  } finally {
    taskLogLoading.value = false
  }
}

watch(taskLogPlug, () => {
  if (taskLogOpen.value) loadTaskLogs()
})

const onTaskLogToggle = (e) => {
  taskLogOpen.value = e.target.open
  if (e.target.open) {
    const key = `${props.device.mac}:${taskLogPlug.value}`
    if (taskLogLoadedFor.value !== key) loadTaskLogs()
  }
}

const formatLogTime = (ts) => {
  if (!ts) return '-'
  return new Date(ts * 1000).toLocaleString('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit'
  })
}

const taskLogTypeText = (entry) => {
  if (entry.type === 'task') return `定时触发（任务 ${(entry.task_idx ?? -1) + 1}）`
  if (entry.type === 'missed') return '未观察到执行'
  return '手动操作（推断）'
}

const taskLogActionText = (entry) => {
  if (entry.type === 'missed') return entry.action === 1 ? '应开启' : '应关闭'
  return entry.cur_on === 1 ? '关 → 开' : '开 → 关'
}


</script>

<template>
  <div class="device-control">
    <div class="device-header">
      <div class="info-row">
        <span>MAC: {{ device.mac }}</span>
        <span>
          <template v-if="isV2">
            {{ device.deviceName || device.mac }}
            <span class="version-tag">v2</span>
          </template>
          <template v-else-if="!deviceNameEditing">
        {{ device.displayName || device.name || 'zTC1' }} <button @click="startDeviceEdit" class="icon-btn">✎</button>
          </template>
          <template v-else>
            <input v-model="tempDeviceName" @keyup.enter="saveDeviceName" class="edit-input">
            <button @click="saveDeviceName" class="icon-btn">✓</button>
          </template>
        </span>
        <span v-if="canChangeFirmware" class="firmware-switch" title="管理员可切换设备固件版本(跨端持久化)">
          <button :class="{ active: device.version === 'v1' }" :disabled="firmwareSaving" @click="applyFirmware('v1')">V1</button>
          <button :class="{ active: device.version === 'v2' }" :disabled="firmwareSaving" @click="applyFirmware('v2')">V2</button>
        </span>
      </div>
      <div v-if="!hasDeviceData" class="no-data-hint">
        ⏳ 尚未收到该设备的任何数据。若长时间无数据，请确认设备在线，或用右上角 V1/V2 切换固件版本（版本标错会导致订阅不到数据）。
      </div>
      <div class="stats-row">
        <div class="stat">
          <label>功率</label>
          <div class="value power-value"><span class="power-main">{{ formatPower }} W</span><span class="monthly-kwh">≈{{ monthlyKwh }}度/月</span></div>
        </div>
        <template v-if="isV2">
          <div class="stat">
            <label>运行时间</label>
            <div class="value">{{ device.startupTime || '-' }}</div>
          </div>
          <div class="stat">
            <label>总耗电量</label>
            <div class="value">{{ device.powerConsumption || '0' }} kWh</div>
          </div>
          <div class="stat">
            <label>今日耗电</label>
            <div class="value">{{ device.powerConsumptionToday || '0' }} kWh</div>
          </div>
          <div class="stat">
            <label>昨日耗电</label>
            <div class="value">{{ device.powerConsumptionYesterday || '0' }} kWh</div>
          </div>
        </template>
        <template v-else>
          <div class="stat">
            <label>运行时间</label>
            <div class="value">{{ formatTime(device.total_time) }}</div>
          </div>
        </template>
        
        <div class="stat-controls">
            <div class="layout-selector">
                <select v-model="persistedLayout" title="插口布局">
                    <option value="auto">自动布局</option>
                    <option value="1x6">1行 (6列)</option>
                    <option value="2x3">2行 (3列)</option>
                    <option value="3x2">3行 (2列)</option>
                </select>
            </div>
            <template v-if="isV2">
              <button 
                class="toggle-chip" 
                :class="{ active: device.total_socket === 1 }"
                @click="toggleTotalSocket"
                title="总开关"
              >
               ⚡ 总开关
              </button>
              <button 
                class="toggle-chip" 
                :class="{ active: device.childLock === 1 }"
                @click="toggleChildLock"
                title="童锁"
              >
               👶 童锁
              </button>
              <button 
                class="toggle-chip" 
                :class="{ active: device.led === 1 }"
                @click="toggleLedLock"
                title="LED指示灯"
              >
               💡 LED
              </button>
              <button 
                class="toggle-chip danger" 
                @click="softRebootDevice"
                title="软重启设备"
              >
               🔃 软重启
              </button>
              <button 
                class="toggle-chip danger" 
                @click="rebootDevice"
                title="硬重启设备"
              >
               🔄 重启
              </button>
            </template>
            <template v-else>
              <button 
                class="toggle-chip" 
                :class="{ active: device.child_lock === 1 }"
                @click="toggleChildLock"
                title="童锁 (按键失效)"
              >
               👶 童锁
              </button>
              <button 
                class="toggle-chip" 
                :class="{ active: device.led_lock === 1 }"
                @click="toggleLedLock"
                title="夜间模式 (关闭LED)"
              >
               🌙 夜间
              </button>
              <button 
                class="toggle-chip"
                :class="{ active: timeSyncState === 'sending' }"
                @click="syncDeviceTime"
                title="同步设备时间（发送当前时间到设备固件）"
              >
               🕐 同步时间
              </button>
            </template>
        </div>
        <div v-if="timeSyncMsg" class="time-sync-msg">{{ timeSyncMsg }}</div>
      </div>
    </div>

    <!-- v2 plugs grid -->
    <div v-if="isV2" class="plugs-grid" :style="gridStyle">
      <div v-for="i in 6" :key="i-1" class="plug-item card" :class="{ on: getV2SocketState(i-1) === 1 }">
        <div class="plug-header">
            <div v-if="editingPlug !== i-1" class="plug-name" @click="startEdit(i-1)" :title="remarks[i-1] || getV2SocketName(i-1)">
              <span class="plug-name-text">{{ remarks[i-1] || getV2SocketName(i-1) }}</span>
              <span v-if="remarks[i-1]" class="remark-indicator" title="本地备注">*</span>
            </div>
          <input v-else 
            v-model="tempName" 
            @blur="saveV2Edit(i-1)" 
            @keyup.enter="saveV2Edit(i-1)"
            class="edit-input"
            autoFocus
          >
        </div>
        <div class="plug-body">
          <button
            class="switch-btn"
            :class="{ active: getV2SocketState(i-1) === 1 }"
            @click="togglePlug(i-1)"
          >
            {{ getV2SocketState(i-1) === 1 ? 'ON' : 'OFF' }}
          </button>

          <div class="plug-actions">
            <button class="text-btn" @click="openTimer(i-1)">
              定时<span v-if="getEnabledTaskCount(i-1) > 0" class="task-badge">{{ getEnabledTaskCount(i-1) }}</span>
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- v1 plugs grid -->
    <div v-else class="plugs-grid" :style="gridStyle">
      <div v-for="i in 6" :key="i-1" class="plug-item card" :class="{ on: getPlug(i-1)?.on === 1 }">
        <div class="plug-header">
          <template v-if="getPlug(i-1)">
            <div v-if="editingPlug !== i-1" class="plug-name" @click="startEdit(i-1)" :title="remarks[i-1] || getPlug(i-1).name || `插口 ${i}`">
              <span class="plug-name-text">{{ remarks[i-1] || getPlug(i-1).name || `插口 ${i}` }}</span>
              <span v-if="remarks[i-1]" class="remark-indicator" title="本地备注">*</span>
            </div>
            <input v-else 
              v-model="tempName" 
              @blur="saveEdit(i-1)" 
              @keyup.enter="saveEdit(i-1)"
              class="edit-input"
              autoFocus
            >
          </template>
          <template v-else>
            <div class="plug-name"><span class="plug-name-text">插口 {{ i }} (未知)</span></div>
          </template>
        </div>
        
        <div class="plug-body">
          <button 
            class="switch-btn" 
            :class="{ active: getPlug(i-1)?.on === 1 }"
            @click="togglePlug(i-1)"
            :disabled="!getPlug(i-1)"
          >
            {{ getPlug(i-1)?.on === 1 ? 'ON' : 'OFF' }}
          </button>
          
          <div class="plug-actions">
            <button class="text-btn" @click="openTimer(i-1)">
              定时<span v-if="getEnabledTaskCount(i-1) > 0" class="task-badge">{{ getEnabledTaskCount(i-1) }}</span>
            </button>
          </div>
        </div>
      </div>
    </div>

    <PowerChart :mac="device.mac" :power="device.power" />

    <TimerModal
      :show="timerPlug !== -1"
      :plug-index="timerPlug"
      :plug-name="timerPlug >= 0 ? getPlugDisplayName(timerPlug) : ''"
      :is-v2="isV2"
      :get-task="getTask"
      :update-task="updateTask"
      :delete-task="deleteTask"
      :save-countdown="saveCountdown"
      :stop-countdown="stopCountdown"
      :countdown-meta="countdownMetaForPlug"
      @close="closeTimer"
    />

    <!-- 简化版，后续可以添加更多设置 -->
    <details v-if="!isV2" class="advanced-settings">
      <summary>高级设置 (设备 MQTT & OTA)</summary>
      <div class="settings-content">
        <div class="settings-group">
            <h4>设备 MQTT 配置 <button @click="querySettings" class="small-btn">查询当前</button></h4>
            <div class="form-row">
                <label>服务器: <input v-model="deviceSettings.mqtt_uri" placeholder="www.mqtt.com"></label>
            </div>
            <div class="form-row">
                <label>端口: <input v-model="deviceSettings.mqtt_port" type="number"></label>
            </div>
            <div class="form-row">
                <label>用户: <input v-model="deviceSettings.mqtt_user"></label>
            </div>
            <div class="form-row">
                <label>密码: <input v-model="deviceSettings.mqtt_password" type="password"></label>
            </div>
            <button @click="saveMqttSettings" class="primary-btn">应用 MQTT 设置</button>
        </div>

        <div class="settings-group">
            <h4>固件升级 (OTA)</h4>
            <div class="form-row">
                <label>URL: <input v-model="deviceSettings.ota" placeholder="http://.../fw.bin"></label>
            </div>
            <button @click="performOta" class="primary-btn">开始升级</button>
        </div>
      </div>
    </details>

    <!-- 定时任务执行日志（推断型） -->
    <details v-if="!isV2" class="advanced-settings task-log-details" @toggle="onTaskLogToggle">
      <summary>定时任务执行日志 <span class="log-fyi-tag">仅供参考</span></summary>
      <div class="settings-content">
        <p class="task-log-disclaimer">
          日志由服务器根据设备状态变化与任务配置自动推断生成，仅供参考，不保证与实际触发完全一致。保留 15 天。
        </p>
        <div class="task-log-toolbar">
          <select v-model="taskLogPlug" class="log-plug-select">
            <option :value="-1">全部插口</option>
            <option v-for="i in 6" :key="i-1" :value="i-1">{{ getPlugDisplayName(i-1) }}</option>
          </select>
          <button class="small-btn" @click="loadTaskLogs" :disabled="taskLogLoading">
            {{ taskLogLoading ? '加载中...' : '刷新' }}
          </button>
        </div>
        <p v-if="taskLogError" class="task-log-error">{{ taskLogError }}</p>
        <div v-else-if="taskLogs.length === 0 && !taskLogLoading" class="task-log-empty">
          暂无记录（设备上线后产生的状态变化会记录在此）
        </div>
        <ul v-else class="task-log-list">
          <li v-for="(entry, idx) in taskLogs" :key="idx" :class="entry.type">
            <span class="tl-time">{{ formatLogTime(entry.ts) }}</span>
            <span class="tl-plug">{{ getPlugDisplayName(entry.plug_idx) }}</span>
            <span class="tl-action">{{ taskLogActionText(entry) }}</span>
            <span class="tl-type" :class="entry.type">{{ taskLogTypeText(entry) }}</span>
          </li>
        </ul>
      </div>
    </details>
  </div>
</template>

<style scoped>
.device-control {
  padding: 10px;
}
.device-header {
  margin-bottom: 20px;
  background: var(--bg-card);
  padding: 15px;
  border-radius: 8px;
  box-shadow: 0 2px 4px var(--shadow-color);
}
.info-row {
  display: flex;
  justify-content: space-between;
  margin-bottom: 15px;
  font-weight: bold;
  flex-wrap: wrap; /* Allow wrapping */
  gap: 10px;
}
.stats-row {
  display: flex;
  gap: 20px;
  align-items: center;
  flex-wrap: wrap; /* Allow wrapping */
}
.stat-controls {
    display: flex;
    gap: 10px;
    margin-left: auto;
    flex-wrap: wrap;
}

/* Mobile optimizations */
@media (max-width: 600px) {
    .stat-controls {
        margin-left: 0;
        width: 100%;
        margin-top: 10px;
        justify-content: space-between;
    }
    .layout-selector {
        width: 100%;
        margin-bottom: 10px;
    }
    .layout-selector select {
        width: 100%;
    }
    .stat {
        flex: 1;
        min-width: 80px;
    }
}
.layout-selector {
    margin-right: 10px;
}
.layout-selector select {
    padding: 5px;
    border-radius: 4px;
    border: 1px solid var(--border-color);
    color: var(--text-secondary);
    background: var(--bg-card);
}
.toggle-chip {
    padding: 6px 12px;
    border-radius: 16px;
    border: 1px solid var(--border-color);
    background: var(--chip-bg);
    cursor: pointer;
    font-size: 0.85em;
    transition: all 0.2s;
    color: var(--chip-color);
}
.toggle-chip.active {
    background: var(--chip-active-bg);
    color: #1976d2;
    border-color: #1976d2;
}
.toggle-chip.danger {
    color: var(--chip-danger-color);
    border-color: #e57373;
}
.toggle-chip.danger:hover {
    background: var(--chip-danger-hover);
}
.version-tag {
    font-size: 0.7em;
    padding: 1px 5px;
    border-radius: 3px;
    background: var(--chip-active-bg);
    color: #1976d2;
    margin-left: 6px;
    vertical-align: middle;
}
.firmware-switch {
    margin-left: 10px;
    display: inline-flex;
    gap: 2px;
    vertical-align: middle;
}
.firmware-switch button {
    font-size: 0.72em;
    padding: 1px 8px;
    border: 1px solid var(--border-color);
    background: var(--bg-secondary);
    color: var(--text-secondary);
    cursor: pointer;
}
.firmware-switch button:first-child { border-radius: 10px 0 0 10px; }
.firmware-switch button:last-child { border-radius: 0 10px 10px 0; }
.firmware-switch button.active {
    background: #1976d2;
    border-color: #1976d2;
    color: #fff;
}
.firmware-switch button:disabled { opacity: 0.6; cursor: wait; }
.no-data-hint {
    font-size: 0.85em;
    color: var(--text-secondary);
    background: var(--bg-secondary);
    border-left: 3px solid #ff9800;
    border-radius: 0 6px 6px 0;
    padding: 8px 12px;
    margin-bottom: 12px;
}
.stat label {
  font-size: 0.8em;
  color: var(--text-secondary);
  display: block;
}
.stat .value {
  font-size: 1.2em;
  font-weight: bold;
}
.power-value {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
}
.power-main {
  white-space: nowrap;
  margin-right: 4px;
}
.monthly-kwh {
  font-size: 0.55em;
  font-weight: normal;
  color: var(--text-muted);
  white-space: nowrap;
}

.plugs-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 15px;
  margin-bottom: 20px;
}
.plug-item {
  background: var(--bg-card);
  border-radius: 8px;
  padding: 15px;
  text-align: center;
  transition: all 0.3s ease;
  border: 2px solid transparent;
  display: flex;
  flex-direction: column;
}
.plug-item.on {
  border-color: var(--card-on-border);
  background: var(--card-on-bg);
}
.plug-header {
  flex-shrink: 0;
}
.plug-body {
  margin-top: auto;
  padding-top: 8px;
}
.plug-name {
  min-height: 2.6em;
  max-height: 2.6em;
  line-height: 1.3;
  margin-bottom: 15px;
  font-weight: 500;
  cursor: pointer;
  border-bottom: 1px dashed var(--border-color);
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-primary);
  padding: 0 2px;
  box-sizing: border-box;
}
.plug-name-text {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
  overflow-wrap: break-word;
  word-break: normal;
  max-width: 100%;
}
.switch-btn {
  width: 60px;
  height: 60px;
  border-radius: 50%;
  border: none;
  background: var(--switch-bg);
  color: var(--switch-color);
  font-weight: bold;
  cursor: pointer;
  transition: all 0.2s;
}
.switch-btn.active {
  background: #4caf50;
  color: white;
  box-shadow: 0 0 10px rgba(76, 175, 80, 0.5);
}

.edit-input {
  width: 100%;
  padding: 4px;
  background: var(--bg-input);
  color: var(--text-primary);
  border: 1px solid var(--border-color);
  border-radius: 4px;
}
.icon-btn {
  background: none;
  border: none;
  cursor: pointer;
  color: var(--text-secondary);
}
.card {
  box-shadow: 0 2px 4px var(--shadow-color);
}

.advanced-settings {
  background: var(--bg-card);
  padding: 10px;
  border-radius: 8px;
  margin-top: 20px;
  border: 1px solid var(--border-color);
  color: var(--text-primary);
}
.settings-content {
    padding: 10px;
}
.settings-group {
    margin-bottom: 20px;
    padding-bottom: 10px;
    border-bottom: 1px solid var(--border-light);
}
.form-row {
    margin-bottom: 10px;
}
.form-row label {
    display: flex;
    flex-direction: column;
    font-size: 0.9em; 
    color: var(--text-secondary);
}
.form-row input {
    padding: 6px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    margin-top: 4px;
    background: var(--bg-input);
    color: var(--text-primary);
}
.small-btn {
    font-size: 0.8em;
    padding: 2px 8px;
    background: var(--bg-secondary);
    border: none;
    border-radius: 4px;
    cursor: pointer;
    margin-left: 10px;
    color: var(--text-primary);
}
.primary-btn {
    background: #2196f3;
    color: white;
    border: none;
    padding: 8px 16px;
    border-radius: 4px;
    cursor: pointer;
}
.plug-actions {
    margin-top: 12px;
}
.text-btn {
    border: 1px solid var(--border-color);
    background: var(--bg-card);
    color: #2196f3;
    cursor: pointer;
    font-size: 0.8em;
    padding: 5px 14px;
    border-radius: 6px;
    display: inline-flex;
    align-items: center;
    gap: 5px;
    transition: all 0.2s;
}
.text-btn:hover {
    border-color: #2196f3;
    background: var(--bg-hover);
}
.task-badge {
    background: #2196f3;
    color: white;
    font-size: 0.75em;
    line-height: 1;
    padding: 2px 5px;
    border-radius: 8px;
    font-weight: bold;
}
.time-sync-msg {
    font-size: 0.8em;
    color: #4caf50;
    background: rgba(76, 175, 80, 0.08);
    padding: 6px 10px;
    border-radius: 4px;
    margin-top: 8px;
    width: 100%;
}

/* 定时任务执行日志 */
.task-log-details {
  margin-bottom: 20px;
}
.log-fyi-tag {
  font-size: 0.7em;
  padding: 1px 6px;
  border-radius: 8px;
  background: rgba(255, 152, 0, 0.15);
  color: #f57c00;
  margin-left: 6px;
  vertical-align: middle;
}
.task-log-disclaimer {
  font-size: 0.78em;
  color: var(--text-muted);
  background: var(--bg-secondary);
  border-left: 3px solid #ff9800;
  border-radius: 0 6px 6px 0;
  padding: 8px 12px;
  margin: 0 0 10px;
}
.task-log-toolbar {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}
.log-plug-select {
  padding: 5px 8px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 0.85em;
}
.task-log-error {
  font-size: 0.85em;
  color: #e53935;
  margin: 0;
}
.task-log-empty {
  font-size: 0.85em;
  color: var(--text-muted);
  text-align: center;
  padding: 12px 0;
}
.task-log-list {
  list-style: none;
  margin: 0;
  padding: 0;
  max-height: 320px;
  overflow-y: auto;
}
.task-log-list li {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 10px;
  border-bottom: 1px solid var(--border-light);
  font-size: 0.85em;
  flex-wrap: wrap;
}
.task-log-list li:last-child { border-bottom: none; }
.tl-time {
  color: var(--text-muted);
  font-variant-numeric: tabular-nums;
  flex: none;
  min-width: 92px;
}
.tl-plug {
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 120px;
}
.tl-action {
  font-variant-numeric: tabular-nums;
  color: var(--text-secondary);
}
.tl-type {
  margin-left: auto;
  font-size: 0.9em;
  padding: 1px 8px;
  border-radius: 10px;
  white-space: nowrap;
}
.tl-type.task {
  color: #4caf50;
  background: rgba(76, 175, 80, 0.12);
}
.tl-type.manual {
  color: var(--text-secondary);
  background: var(--bg-secondary);
}
.tl-type.missed {
  color: #f57c00;
  background: rgba(255, 152, 0, 0.12);
}
</style>
