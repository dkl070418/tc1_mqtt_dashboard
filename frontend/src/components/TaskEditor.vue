<script setup>
import { reactive, watch, ref } from 'vue'

const props = defineProps({
  task: Object,
  index: Number,
  countdownActive: Boolean,  // 该槽位正被倒计时占用（仅任务5）
  deletable: { type: Boolean, default: false }
})

const emit = defineEmits(['save', 'delete'])

const editing = ref(false)
const localTask = reactive({
  hour: 0,
  minute: 0,
  repeat: 0,
  action: 1,
  on: 0
})

const days = ['一', '二', '三', '四', '五', '六', '日']

// Init local state when editing starts or props change
watch(() => props.task, (val) => {
  if (val) {
    Object.assign(localTask, val)
  }
}, { immediate: true })

const toggleDay = (dayIndex) => {
  // Flip bit at dayIndex
  localTask.repeat ^= (1 << dayIndex)
}

const isDaySelect = (dayIndex) => {
  return (localTask.repeat >> dayIndex) & 1
}

const save = () => {
  emit('save', { ...localTask })
  editing.value = false
}

const startEdit = () => {
    // If no task exists yet, init default
    if (!props.task) {
        Object.assign(localTask, {
            hour: 12, minute: 0, repeat: 0, action: 1, on: 1
        })
    } else {
        Object.assign(localTask, props.task)
    }
    editing.value = true
}

const cancel = () => {
    editing.value = false
}

const formatRepeat = (bits) => {
  if (bits === 0) return '一次'
  if (bits === 127) return '每天'
  let res = []
  for(let i=0; i<7; i++) {
    if ((bits >> i) & 1) res.push(days[i])
  }
  return res.length > 0 ? '周' + res.join('') : '一次'
}

const pad = (n) => String(n).padStart(2, '0')

// 摘要行直接启停：已存在则翻转 on，否则按默认任务启用
const toggleOn = () => {
  if (props.task) {
    emit('save', { ...props.task, on: props.task.on === 1 ? 0 : 1 })
  } else {
    emit('save', {
      hour: 12, minute: 0, repeat: 0, action: 1, on: 1
    })
  }
}

// 删除任务：停用固件槽位并从配置中移除
const confirmDelete = () => {
  if (confirm(`确定删除任务 ${props.index + 1} 吗？\n\n将停用设备对应槽位并从配置中移除。`)) {
    emit('delete')
  }
}

</script>

<template>
  <div class="task-editor">
    <div v-if="!editing" class="task-summary" @click="startEdit" :title="`点击编辑 任务 ${index + 1}`">
      <div class="ts-row">
        <span class="ts-badge">
          {{ countdownActive ? '⏱ 倒计时' : `任务 ${index + 1}` }}
        </span>
        <template v-if="task && task.on === 1">
          <span class="ts-time">{{ pad(task.hour) }}:{{ pad(task.minute) }}</span>
          <span class="ts-chip" :class="task.action ? 'on' : 'off'">{{ task.action ? '开' : '关' }}</span>
          <span class="ts-dot">·</span>
          <span class="ts-repeat">{{ formatRepeat(task.repeat) }}</span>
        </template>
        <span v-else class="ts-empty">（未启用）</span>
        <span class="ts-spacer"></span>
        <button
          v-if="deletable"
          class="ts-del"
          title="删除此任务"
          @click.stop="confirmDelete"
        >🗑</button>
        <button
          class="ts-switch"
          :class="{ on: task?.on === 1 }"
          :title="task?.on === 1 ? '停用此任务' : '启用此任务'"
          @click.stop="toggleOn"
        ></button>
      </div>
    </div>

    <div v-else class="edit-form">
      <div class="time-picker">
        <input type="number" v-model.number="localTask.hour" min="0" max="23"> :
        <input type="number" v-model.number="localTask.minute" min="0" max="59">
      </div>

      <div class="days-picker">
        <button
            v-for="(d, i) in days"
            :key="i"
            @click="toggleDay(i)"
            :class="{ selected: isDaySelect(i) }"
        >{{ d }}</button>
      </div>

      <div class="options">
        <label>
            动作:
            <select v-model="localTask.action">
                <option :value="1">开启</option>
                <option :value="0">关闭</option>
            </select>
        </label>
        <label>
            启用:
            <input type="checkbox" v-model="localTask.on" :true-value="1" :false-value="0">
        </label>
      </div>

      <div class="actions">
        <button @click="save" class="save-btn">保存</button>
        <button @click="cancel" class="cancel-btn">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.task-editor {
  padding: 6px 0;
}
.task-summary {
  border: 1px solid var(--border-light);
  background: var(--bg-secondary);
  border-radius: 8px;
  padding: 8px 12px;
  cursor: pointer;
  transition: background 0.15s, border-color 0.15s;
}
.task-summary:hover {
  background: var(--bg-hover);
  border-color: var(--border-color);
}
.ts-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.ts-badge {
  font-weight: 600;
  font-size: 0.95em;
  color: var(--text-primary);
  flex: none;
}
.ts-spacer {
  flex: 1;
}
.ts-time { font-weight: bold; color: var(--text-primary); flex: none; }
.ts-chip {
  font-size: 0.85em;
  padding: 0 5px;
  border-radius: 3px;
  line-height: 1.4;
  flex: none;
}
.ts-chip.on { color: #4caf50; background: rgba(76, 175, 80, 0.12); }
.ts-chip.off { color: #e53935; background: rgba(229, 57, 53, 0.12); }
.ts-dot { color: var(--text-muted); flex: none; }
.ts-repeat { color: var(--text-muted); flex: none; }
.ts-empty { color: var(--text-muted); }

.ts-switch {
  width: 36px;
  height: 20px;
  border-radius: 10px;
  border: 1px solid var(--border-color);
  background: var(--bg-card);
  position: relative;
  padding: 0;
  cursor: pointer;
  transition: background 0.2s, border-color 0.2s;
  flex: none;
}
.ts-del {
  border: none;
  background: none;
  cursor: pointer;
  font-size: 0.95em;
  padding: 2px 4px;
  opacity: 0.55;
  flex: none;
}
.ts-del:hover { opacity: 1; }
.ts-switch::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--text-muted);
  transition: left 0.2s, background 0.2s;
}
.ts-switch.on {
  background: #4caf50;
  border-color: #4caf50;
}
.ts-switch.on::after {
  left: 18px;
  background: #fff;
}

.edit-form {
    padding: 10px;
    background: var(--bg-card);
    border: 1px solid var(--border-color);
    margin-top: 5px;
}
.time-picker input {
    width: 40px;
    text-align: center;
    background: var(--bg-input);
    color: var(--text-primary);
    border: 1px solid var(--border-color);
    border-radius: 4px;
}
.days-picker {
    display: flex;
    gap: 2px;
    margin: 10px 0;
}
.days-picker button {
    flex: 1;
    padding: 4px 0;
    font-size: 10px;
    border: 1px solid var(--border-color);
    background: var(--bg-card);
    color: var(--text-primary);
    cursor: pointer;
}
.days-picker button.selected {
    background: #2196f3;
    color: white;
    border-color: #1976d2;
}
.options {
    display: flex;
    justify-content: space-between;
    margin-bottom: 10px;
    font-size: 0.9em;
    color: var(--text-primary);
}
.options select {
    background: var(--bg-input);
    color: var(--text-primary);
    border: 1px solid var(--border-color);
    border-radius: 4px;
}
.actions {
    display: flex;
    gap: 10px;
}
.actions button {
    flex: 1;
    cursor: pointer;
}
.save-btn { background: #4caf50; color: white; border: none; padding: 5px;}
.cancel-btn { background: var(--bg-secondary); border: none; padding: 5px; color: var(--text-primary); }
</style>
