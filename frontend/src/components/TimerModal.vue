<script setup>
import { ref } from 'vue'
import TaskEditor from './TaskEditor.vue'

const props = defineProps({
  show: Boolean,
  plugIndex: Number,
  plugName: String,
  getTask: Function,
  updateTask: Function
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
            <TaskEditor
              v-for="n in slots"
              :key="n"
              :task="getTask ? getTask(plugIndex, n) : null"
              :index="n"
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
</style>
