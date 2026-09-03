<script setup>
import { ref, computed, onUnmounted } from 'vue'
import TaskEditor from './TaskEditor.vue'

const props = defineProps({
  show: Boolean,
  plugIndex: Number,
  plugName: String,
  getTask: Function,
  updateTask: Function,
  saveCountdown: Function,   // (hours, minutes, action) => 保存倒计时(含时间同步)
  stopCountdown: Function,   // 停止当前倒计时
  countdownMeta: Object      // { target_ts, action } | null
})

const emit = defineEmits(['close'])

const close = () => emit('close')

const enabledCount = () => {
  let c = 0
  for (let i = 0; i < 5; i++) {
    const t = props.getTask ? props.getTask(props.plugIndex, i) : null
    if (t && t.on === 1) c++
  }
  return c
}

const slots = [0, 1, 2, 3, 4]

// ---- 倒计时 ----
const cdHours = ref(1)
const cdMinutes = ref(0)
const cdAction = ref(1)
const cdSaving = ref(false)
const cdError = ref('')
const nowTs = ref(Math.floor(Date.now() / 1000))
const tickTimer = setInterval(() => { nowTs.value = Math.floor(Date.now() / 1000) }, 1000)
onUnmounted(() => clearInterval(tickTimer))

const presets = [1, 2, 4, 8]

const applyPreset = (h) => {
  cdHours.value = h
  cdMinutes.value = 0
}

const countdownActive = computed(() =>
  props.countdownMeta && props.countdownMeta.target_ts > nowTs.value
)

const countdownFinished = computed(() =>
  props.countdownMeta && props.countdownMeta.target_ts <= nowTs.value
)

const countdownRemain = computed(() => {
  if (!countdownActive.value) return ''
  const s = props.countdownMeta.target_ts - nowTs.value
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const pad = n => String(n).padStart(2, '0')
  return `${pad(h)}:${pad(m)}:${pad(sec)}`
})

const countdownActionText = computed(() =>
  props.countdownMeta?.action === 1 ? '开启' : '关闭'
)

const saveCountdown = async () => {
  cdError.value = ''
  const h = Number(cdHours.value) || 0
  const m = Number(cdMinutes.value) || 0
  if (h === 0 && m === 0) {
    cdError.value = '请设置倒计时时长（小时/分钟不能全为 0）'
    return
  }
  if (h > 23 || m > 59 || h < 0 || m < 0) {
    cdError.value = '小时 0-23，分钟 0-59'
    return
  }
  cdSaving.value = true
  try {
    await props.saveCountdown?.(h, m, cdAction.value)
  } catch (e) {
    cdError.value = e?.message || '保存失败，请重试'
  } finally {
    cdSaving.value = false
  }
}

const stopCountdown = () => {
  props.stopCountdown?.()
}
</script>

<template>
  <teleport to="body">
    <Transition name="tm-fade">
      <div v-if="show" class="tm-overlay" @click.self="close">
        <div class="tm-modal" role="dialog" aria-modal="true">
          <div class="tm-header">
            <h3 class="tm-title">「{{ plugName }}」定时任务</h3>
            <button class="tm-close" @click="close" title="关闭">×</button>
          </div>
          <p class="tm-hint">最多 5 个定时任务 · 点击任务行展开编辑 · 开关可直接启用/停用</p>
          <div class="tm-list">
            <div class="tm-countdown">
              <div class="cd-title-row">
                <span class="cd-title">⏱ 倒计时（占用任务 5）</span>
                <span v-if="countdownActive" class="cd-remain">剩 {{ countdownRemain }} → {{ countdownActionText }}</span>
              </div>
              <div v-if="countdownActive" class="cd-running">
                <p class="cd-running-text">
                  倒计时进行中，将在 <strong>{{ countdownRemain }}</strong> 后<strong>{{ countdownActionText }}</strong>此插口
                </p>
                <p class="cd-running-hint">由设备固件执行，关闭页面不影响；任务 5 已被倒计时占用</p>
                <button class="cd-stop" @click="stopCountdown">停止倒计时</button>
              </div>
              <div v-else-if="countdownFinished" class="cd-finished">
                <p class="cd-finished-text">⏰ 倒计时已到执行时间，任务 5 仍处于启用状态</p>
                <p class="cd-running-hint">设备固件执行一次性任务后不会自动停用，请点击清除以停用任务 5</p>
                <button class="cd-stop" @click="stopCountdown">清除任务 5</button>
              </div>
              <div v-else class="cd-form">
                <div class="cd-presets">
                  <span class="cd-label">快速选择:</span>
                  <button
                    v-for="h in presets"
                    :key="h"
                    class="cd-preset"
                    :class="{ active: cdHours === h && cdMinutes === 0 }"
                    @click="applyPreset(h)"
                  >{{ h }}小时</button>
                </div>
                <div class="cd-inputs">
                  <label>
                    小时
                    <input type="number" v-model.number="cdHours" min="0" max="23">
                  </label>
                  <label>
                    分钟
                    <input type="number" v-model.number="cdMinutes" min="0" max="59">
                  </label>
                  <label>
                    动作
                    <select v-model.number="cdAction">
                      <option :value="1">开启</option>
                      <option :value="0">关闭</option>
                    </select>
                  </label>
                </div>
                <p class="cd-hint">保存后自动同步设备时间，到点由设备固件执行（一次性任务，直接覆盖任务 5）</p>
                <p v-if="cdError" class="cd-error">{{ cdError }}</p>
                <button class="cd-save" :disabled="cdSaving" @click="saveCountdown">
                  {{ cdSaving ? '保存中...' : '启动倒计时' }}
                </button>
              </div>
            </div>

            <TaskEditor
              v-for="n in slots"
              :key="n"
              :task="getTask ? getTask(plugIndex, n) : null"
              :index="n"
              :countdown-active="n === 4 && countdownActive"
              @save="data => updateTask && updateTask(plugIndex, n, data)"
            />
          </div>
          <div class="tm-footer">
            <span>已启用 {{ enabledCount() }}/5</span>
            <button class="tm-done" @click="close">完成</button>
          </div>
        </div>
      </div>
    </Transition>
  </teleport>
</template>

<style scoped>
.tm-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  background: var(--overlay-bg);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 1000;
  padding: 10px;
}
.tm-modal {
  background: var(--bg-card);
  border-radius: 10px;
  width: 92vw;
  max-width: 420px;
  max-height: 82vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 8px 24px var(--shadow-color);
  color: var(--text-primary);
}
.tm-header {
  padding: 14px 16px;
  border-bottom: 1px solid var(--border-light);
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
}
.tm-title {
  margin: 0;
  font-size: 1.05em;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tm-close {
  background: none;
  border: none;
  font-size: 1.5em;
  line-height: 1;
  cursor: pointer;
  color: var(--text-secondary);
  padding: 0 4px;
}
.tm-close:hover { color: var(--text-primary); }
.tm-hint {
  margin: 0;
  padding: 8px 16px;
  font-size: 0.78em;
  color: var(--text-muted);
  background: var(--bg-secondary);
  border-bottom: 1px solid var(--border-light);
  flex-shrink: 0;
}
.tm-list {
  overflow-y: auto;
  padding: 8px 12px 4px;
  flex: 1;
  min-height: 0;
}
.tm-footer {
  padding: 10px 16px;
  border-top: 1px solid var(--border-light);
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
  font-size: 0.85em;
  color: var(--text-muted);
}
.tm-done {
  padding: 6px 18px;
  border-radius: 16px;
  border: none;
  background: #2196f3;
  color: white;
  cursor: pointer;
  font-size: 0.9em;
}
.tm-fade-enter-active, .tm-fade-leave-active {
  transition: opacity 0.2s;
}
.tm-fade-enter-from, .tm-fade-leave-to {
  opacity: 0;
}

/* 倒计时卡片 */
.tm-countdown {
  border: 1px solid #2196f3;
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 10px;
  background: rgba(33, 150, 243, 0.06);
}
.cd-title-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.cd-title {
  font-weight: 600;
  font-size: 0.92em;
}
.cd-remain {
  font-size: 0.85em;
  color: #2196f3;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.cd-running-text { margin: 8px 0 4px; font-size: 0.9em; }
.cd-running-text strong { color: #2196f3; font-variant-numeric: tabular-nums; }
.cd-running-hint {
  margin: 0 0 8px;
  font-size: 0.75em;
  color: var(--text-muted);
}
.cd-stop {
  padding: 6px 16px;
  border-radius: 6px;
  border: 1px solid #e53935;
  background: none;
  color: #e53935;
  cursor: pointer;
  font-size: 0.85em;
}
.cd-stop:hover { background: rgba(229, 57, 53, 0.08); }
.cd-finished-text {
  margin: 8px 0 4px;
  font-size: 0.9em;
  color: #f57c00;
  font-weight: 600;
}
.cd-presets {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 8px 0;
  flex-wrap: wrap;
}
.cd-label {
  font-size: 0.8em;
  color: var(--text-muted);
}
.cd-preset {
  padding: 4px 10px;
  border-radius: 14px;
  border: 1px solid var(--border-color);
  background: var(--bg-card);
  color: var(--text-primary);
  cursor: pointer;
  font-size: 0.82em;
}
.cd-preset.active {
  background: #2196f3;
  border-color: #2196f3;
  color: white;
}
.cd-inputs {
  display: flex;
  gap: 8px;
  margin: 8px 0;
}
.cd-inputs label {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.82em;
  color: var(--text-secondary);
  min-width: 0;
}
.cd-inputs input,
.cd-inputs select {
  width: 100%;
  min-width: 0;
  padding: 6px;
  border: 1px solid var(--border-color);
  border-radius: 4px;
  background: var(--bg-input);
  color: var(--text-primary);
  font-size: 0.95em;
}
.cd-hint {
  margin: 0 0 8px;
  font-size: 0.72em;
  color: var(--text-muted);
}
.cd-error {
  margin: 0 0 8px;
  font-size: 0.8em;
  color: #e53935;
}
.cd-save {
  width: 100%;
  padding: 8px;
  border-radius: 6px;
  border: none;
  background: #2196f3;
  color: white;
  cursor: pointer;
  font-size: 0.9em;
}
.cd-save:disabled { opacity: 0.6; cursor: not-allowed; }
</style>
