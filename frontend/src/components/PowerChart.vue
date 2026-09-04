<script setup>
import { ref, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { createChart, AreaSeries, ColorType } from 'lightweight-charts'

const props = defineProps({
  mac: {
    type: String,
    required: true
  },
  power: {
    type: Number,
    default: 0
  }
})

const containerRef = ref(null)
const rangeRef = ref('1d')
const granularityRef = ref('auto')
const summaryRef = ref(null)
const loadingRef = ref(false)
const errorRef = ref('')
const chipBrokenRef = ref(false)
const powerEnabledRef = ref(true)   // 后端功耗采集是否启用
const hasDataRef = ref(false)       // 当前时间段是否有数据

let chart = null
let series = null
let currentToken = 0
let powerDebounceTimer = null

const RANGES = [
  { key: '1h', label: '1小时', seconds: 3600, granularity: '1min' },
  { key: '6h', label: '6小时', seconds: 21600, granularity: '1min' },
  { key: '1d', label: '24小时', seconds: 86400, granularity: '1min' },
  { key: '7d', label: '7天', seconds: 604800, granularity: '1h' },
  { key: '30d', label: '30天', seconds: 2592000, granularity: '1h' },
]

const initChart = () => {
  if (!containerRef.value) return
  chart = createChart(containerRef.value, {
layout: {
        background: { type: ColorType.Solid, color: 'transparent' },
        textColor: '#999',
        attributionLogo: false,
      },
    grid: {
      vertLines: { color: 'rgba(128,128,128,0.1)' },
      horzLines: { color: 'rgba(128,128,128,0.1)' },
    },
    rightPriceScale: {
      borderColor: 'rgba(128,128,128,0.3)',
    },
    timeScale: {
      borderColor: 'rgba(128,128,128,0.3)',
      timeVisible: true,
      secondsVisible: false,
    },
    crosshair: {
      mode: 0,
    },
    width: containerRef.value.clientWidth,
    height: 300,
  })
  series = chart.addSeries(AreaSeries, {
    lineColor: '#2196f3',
    topColor: 'rgba(33,150,243,0.3)',
    bottomColor: 'rgba(33,150,243,0.0)',
    lineWidth: 2,
    priceFormat: { type: 'price', precision: 1, minMove: 0.1 },
  })
}

const setRange = (r) => {
  rangeRef.value = r.key
  loadData()
}
const loadSummary = async () => {
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) return
  try {
    const res = await fetch(`/api/admin/power/summary?mac=${props.mac}`, {
      headers: { Authorization: `Bearer ${token}` }
    })
    if (res.ok) {
      const data = await res.json()
      summaryRef.value = data
      powerEnabledRef.value = data.enabled !== false
    }
  } catch (e) {
    console.warn('加载功耗统计失败', e)
  }
}

// silent=true: 后台自动刷新（功率变化节流触发），不显示"加载中"遮罩，
// 失败也不弹错误覆盖旧图，避免每 5 秒闪一次干扰视线
const loadData = async (silent = false) => {
  if (chipBrokenRef.value) return
  const token = localStorage.getItem('mqtt_admin_token')
  if (!token) {
    if (!silent) errorRef.value = '未登录管理员，无法查看功耗'
    return
  }
  if (!silent) {
    loadingRef.value = true
    errorRef.value = ''
  }
  const myToken = ++currentToken

  const range = RANGES.find(r => r.key === rangeRef.value) || RANGES[2]
  const now = Math.floor(Date.now() / 1000)
  const from = now - range.seconds

  try {
    const res = await fetch(
      `/api/admin/power/query?mac=${props.mac}&from=${from}&to=${now}&granularity=${range.granularity}`,
      { headers: { Authorization: `Bearer ${token}` } }
    )
    if (!res.ok) {
      const data = await res.json().catch(() => ({}))
      if (!silent) errorRef.value = data.error || '加载失败'
      return
    }
    const data = await res.json()
    if (myToken !== currentToken) return

    const isHourly = range.granularity === '1h'
    // lightweight-charts 时间轴按 UTC 渲染；此处把 unix 秒换算成浏览器本地时区
    // （lightweight-charts 收到的时间戳当 UTC 显示，所以 +本地偏移 后显示的即是本地时间）
    const tzOffset = -new Date().getTimezoneOffset() * 60 // 秒
    const points = (data || []).map(d => {
      const rawTs = parseInt(d.bucket_ts || d.ts)
      const ts = rawTs + tzOffset
      const value = isHourly ? (d.avg_w || 0) : (d.avg_w !== undefined ? d.avg_w : d.power_w || 0)
      return { time: ts, value: parseFloat(value.toFixed(1)) }
    }).filter(p => p.time > 0)

    if (!series || !chart) initChart()
    if (!series || !chart) {
      if (!silent) errorRef.value = '图表初始化失败'
      return
    }
    series.setData(points)
    hasDataRef.value = points.length > 0
    chart.timeScale().fitContent()
  } catch (e) {
    if (!silent) errorRef.value = '网络错误: ' + e.message
    else console.warn('功耗数据后台刷新失败', e)
  } finally {
    if (!silent && myToken === currentToken) loadingRef.value = false
  }
}

const formatKwh = (v) => {
  const n = parseFloat(v || 0)
  return n >= 100 ? n.toFixed(0) : n.toFixed(2)
}

const updateChipState = (val) => {
  chipBrokenRef.value = val < 0
  if (val < 0) {
    errorRef.value = '该设备疑似功耗芯片损坏，不进行统计'
    loadingRef.value = false
  } else {
    errorRef.value = ''
    nextTick(() => {
      loadData()
    })
  }
}

watch(() => props.mac, () => {
  summaryRef.value = null
  updateChipState(props.power)
  loadSummary()
})

onMounted(() => {
  updateChipState(props.power)
  loadSummary()
  window.addEventListener('resize', handleResize)
})

onUnmounted(() => {
  window.removeEventListener('resize', handleResize)
  if (powerDebounceTimer) { clearTimeout(powerDebounceTimer); powerDebounceTimer = null }
  if (chart) {
    chart.remove()
    chart = null
    series = null
  }
})

const handleResize = () => {
  if (chart && containerRef.value) {
    chart.applyOptions({ width: containerRef.value.clientWidth })
  }
}

watch(() => props.power, (val) => {
  chipBrokenRef.value = val < 0
  if (val < 0) {
    errorRef.value = '该设备疑似功耗芯片损坏，不进行统计'
    loadingRef.value = false
    return
  }
  errorRef.value = ''
  // 节流：功率值频繁变化时，最多每 5 秒静默重载一次图表数据（不闪"加载中"）
  if (powerDebounceTimer) return
  powerDebounceTimer = setTimeout(() => {
    powerDebounceTimer = null
    loadData(true)
  }, 5000)
})
</script>

<template>
  <div class="power-chart">
    <div v-if="chipBrokenRef" class="chip-broken">
      ⚠️ 该设备疑似功耗芯片损坏，不进行统计
    </div>
    <div v-else-if="!powerEnabledRef" class="chip-broken">
      📊 功耗采集未启用：后端未开启 <code>power_collect_enabled</code>，或未配置 <code>power_db_path</code>，此设备暂无功耗数据。
    </div>
    <div v-show="chipBrokenRef" style="display:none"></div>
    <div class="power-header" v-show="powerEnabledRef && !chipBrokenRef">
      <div class="power-summary" v-if="summaryRef">
        <div class="summary-item">
          <span class="summary-label">今日用电</span>
          <span class="summary-value">{{ formatKwh(summaryRef.today_energy_kwh) }} kWh</span>
        </div>
        <div class="summary-item">
          <span class="summary-label">本月用电</span>
          <span class="summary-value">{{ formatKwh(summaryRef.month_energy_kwh) }} kWh</span>
        </div>
      </div>
      <div class="power-ranges">
        <button
          v-for="r in RANGES"
          :key="r.key"
          class="range-btn"
          :class="{ active: rangeRef === r.key }"
          @click="setRange(r)"
        >{{ r.label }}</button>
      </div>
    </div>

    <div class="chart-wrapper" v-show="powerEnabledRef && !chipBrokenRef">
      <div ref="containerRef" class="chart-container"></div>
      <div v-show="loadingRef" class="chart-overlay">加载中...</div>
      <div v-show="!loadingRef && !hasDataRef" class="chart-overlay chart-empty">
        所选时间段暂无功耗数据（设备可能未产生或已过清理周期）
      </div>
      <div v-show="errorRef && !chipBrokenRef" class="chart-overlay chart-error">{{ errorRef }}</div>
    </div>
  </div>
</template>

<style scoped>
.power-chart {
  margin-top: 20px;
  padding: 15px;
  background: var(--bg-card);
  border-radius: 8px;
  box-shadow: 0 2px 4px var(--shadow-color);
}
.power-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 15px;
}
.power-summary {
  display: flex;
  gap: 24px;
  flex-wrap: wrap;
  min-height: 2.8em;
}
.summary-item {
  display: flex;
  flex-direction: column;
}
.summary-label {
  font-size: 0.75em;
  color: var(--text-muted, #888);
}
.summary-value {
  font-size: 1.3em;
  font-weight: bold;
  color: var(--text-primary);
}
.power-ranges {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
}
.range-btn {
  padding: 5px 12px;
  border: 1px solid var(--border-color);
  border-radius: 14px;
  background: var(--bg-secondary);
  color: var(--text-secondary);
  cursor: pointer;
  font-size: 0.85em;
}
.range-btn.active {
  background: var(--chip-active-bg, #e3f2fd);
  color: #1976d2;
  border-color: #1976d2;
}
.chart-wrapper {
  position: relative;
  width: 100%;
  height: 300px;
}
.chart-container {
  width: 100%;
  height: 100%;
}
.chart-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 0.9em;
  background: var(--bg-card);
  pointer-events: none;
}
.chart-empty {
  font-size: 0.85em;
  color: var(--text-muted);
}
.chart-error {
  color: #f44336;
}
.power-error {
  color: #f44336;
  font-size: 0.9em;
  text-align: center;
  padding: 10px;
}
.chip-broken {
  padding: 20px;
  text-align: center;
  color: #f44336;
  background: rgba(244, 67, 54, 0.06);
  border: 1px dashed #f44336;
  border-radius: 8px;
  font-size: 1em;
}
</style>
