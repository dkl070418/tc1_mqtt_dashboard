<script setup>
import { ref, nextTick, onBeforeUnmount, onMounted } from 'vue'
import QRCode from 'qrcode'
import { Html5QrcodeScanner, Html5Qrcode } from 'html5-qrcode'

const emit = defineEmits(['config-imported'])

const showModal = ref(false)
const activeTab = ref('export') // 'export', 'scan', 'paste'
const exportText = ref('')
const shareUrl = ref('')
const importText = ref('')
const qrCanvas = ref(null)
const scanError = ref('')
const importStatus = ref('')
const copySuccess = ref(false)

// ---- 密码保护（v1.5.1）----
const isAdmin = ref(false)          // 管理员登录期间导出：彻底剥离密码
const includePassword = ref(false)  // 普通用户：是否随码携带密码
const encPass = ref('')             // 导出用迁移口令
const encPass2 = ref('')
const exportError = ref('')
const pendingEnc = ref(null)        // 导入待解密的配置
const importPass = ref('')
const importPassError = ref('')

let html5QrcodeScanner = null

const encodeToBase64Url = (str) => {
    const bytes = new TextEncoder().encode(str)
    let binary = ''
    bytes.forEach((b) => { binary += String.fromCharCode(b) })
    return btoa(binary)
        .replace(/\+/g, '-')
        .replace(/\//g, '_')
        .replace(/=+$/g, '')
}

const decodeFromBase64Url = (str) => {
    const padded = str.replace(/-/g, '+').replace(/_/g, '/')
    const padLen = (4 - (padded.length % 4)) % 4
    const base64 = padded + '='.repeat(padLen)
    const binary = atob(base64)
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0))
    return new TextDecoder().decode(bytes)
}

// ---- Web Crypto: PBKDF2 + AES-GCM ----
const PBKDF2_ITERATIONS = 100000
const textEnc = new TextEncoder()
const textDec = new TextDecoder()

const b64ToBuf = (b64) => Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
const bufToB64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf)))

const deriveKey = async (passphrase, saltBuf) => {
    const keyMaterial = await crypto.subtle.importKey(
        'raw', textEnc.encode(passphrase), 'PBKDF2', false, ['deriveKey']
    )
    return crypto.subtle.deriveKey(
        { name: 'PBKDF2', salt: saltBuf, iterations: PBKDF2_ITERATIONS, hash: 'SHA-256' },
        keyMaterial,
        { name: 'AES-GCM', length: 256 },
        false,
        ['encrypt', 'decrypt']
    )
}

// 加密敏感字段: 返回 { v, s:盐, i:IV, d:密文 }（均为 base64）
const encryptSecret = async (passphrase, obj) => {
    const salt = crypto.getRandomValues(new Uint8Array(16))
    const iv = crypto.getRandomValues(new Uint8Array(12))
    const key = await deriveKey(passphrase, salt)
    const ct = await crypto.subtle.encrypt(
        { name: 'AES-GCM', iv },
        key,
        textEnc.encode(JSON.stringify(obj))
    )
    return { v: 1, s: bufToB64(salt), i: bufToB64(iv), d: bufToB64(ct) }
}

const decryptSecret = async (passphrase, encField) => {
    const salt = b64ToBuf(encField.s)
    const iv = b64ToBuf(encField.i)
    const ct = b64ToBuf(encField.d)
    const key = await deriveKey(passphrase, salt)
    const pt = await crypto.subtle.decrypt({ name: 'AES-GCM', iv }, key, ct)
    return JSON.parse(textDec.decode(pt))
}

const copyToClipboard = async (text) => {
    try {
        // Try modern API first
        if (navigator.clipboard && window.isSecureContext) {
            await navigator.clipboard.writeText(text)
        } else {
            // Fallback to older method
            const textarea = document.createElement('textarea')
            textarea.value = text
            textarea.style.position = 'fixed'
            textarea.style.opacity = '0'
            document.body.appendChild(textarea)
            textarea.select()
            document.execCommand('copy')
            document.body.removeChild(textarea)
        }
        copySuccess.value = true
        setTimeout(() => { copySuccess.value = false }, 2000)
    } catch (err) {
        console.error('Failed to copy:', err)
        alert('复制失败，请重试')
    }
}

const buildShareUrl = (jsonStr) => {
    const encoded = encodeToBase64Url(jsonStr)
    const base = `${window.location.origin}${window.location.pathname}`
    return `${base}?cfg=${encoded}`
}

const clearUrlConfigParam = () => {
    const url = new URL(window.location.href)
    if (url.searchParams.has('cfg')) {
        url.searchParams.delete('cfg')
        window.history.replaceState({}, '', url.toString())
    }
}

const close = () => {
  stopScanner()
  showModal.value = false
  importStatus.value = ''
}

const stopScanner = () => {
    if (html5QrcodeScanner) {
        try {
             html5QrcodeScanner.clear().catch(err => console.error(err))
        } catch(e) {}
        html5QrcodeScanner = null
    }
}

const clearQr = () => {
    if (qrCanvas.value) {
        const ctx = qrCanvas.value.getContext('2d')
        ctx.clearRect(0, 0, qrCanvas.value.width, qrCanvas.value.height)
    }
}

// ---- 导出生成（明文/二维码/链接同源）----
let exportSeq = 0
const regenerateExport = async () => {
    const seq = ++exportSeq
    exportError.value = ''
    exportText.value = ''
    shareUrl.value = ''
    clearQr()

    let mqtt = {}
    try { mqtt = JSON.parse(localStorage.getItem('mqtt_config') || '{}') } catch (e) {}

    // 无条件剥离密码；仅普通用户勾选"包含密码"时以密文携带
    const safeMqtt = {}
    if (mqtt.username) safeMqtt.username = mqtt.username
    if (mqtt.host) safeMqtt.host = mqtt.host
    if (mqtt.clientId) safeMqtt.clientId = mqtt.clientId

    if (includePassword.value && !isAdmin.value) {
        if (!mqtt.password) {
            exportError.value = '本地未保存 MQTT 密码，无法包含密码导出'
            return
        }
        if (!encPass.value || !encPass2.value) {
            exportError.value = '请输入并确认迁移口令'
            return
        }
        if (encPass.value !== encPass2.value) {
            exportError.value = '两次输入的迁移口令不一致'
            return
        }
        try {
            safeMqtt.enc = await encryptSecret(encPass.value, {
                username: mqtt.username || '',
                password: mqtt.password
            })
        } catch (e) {
            exportError.value = '加密失败: ' + (e.message || e)
            return
        }
        if (seq !== exportSeq) return
    }

    const remarks = {}
    for (let i = 0; i < localStorage.length; i++) {
        const key = localStorage.key(i)
        if (key && key.startsWith('ztc1_remarks_')) {
            try {
               remarks[key] = JSON.parse(localStorage.getItem(key))
            } catch(e) {}
        }
    }

    const data = {
        timestamp: Date.now(),
        mqtt: safeMqtt,
        devices: JSON.parse(localStorage.getItem('ztc1_devices') || '[]'),
        remarks,
        auto_connect: localStorage.getItem('mqtt_auto_connect')
    }

    const str = JSON.stringify(data)
    if (seq !== exportSeq) return
    exportText.value = str
    shareUrl.value = buildShareUrl(str)
    await nextTick()
    if (seq !== exportSeq) return
    if (qrCanvas.value) {
        QRCode.toCanvas(qrCanvas.value, str, { width: 200, margin: 2 }, (error) => {
            if (error) console.error(error)
        })
    }
}

const switchTab = async (tab) => {
    // Cleanup prev tab
    if (activeTab.value === 'scan') {
        stopScanner()
    }

    activeTab.value = tab
    importStatus.value = ''

    if (tab === 'export') {
        await regenerateExport()
    } else if (tab === 'scan') {
        await nextTick()
        startScanner()
    }
}

const startScanner = () => {
    // Use a small timeout to ensure DOM is ready
    setTimeout(() => {
        const onScanSuccess = (decodedText, decodedResult) => {
            console.log(`Code matched = ${decodedText}`, decodedResult)
            stopScanner()
            handleImportPayload(decodedText)
        }

        const onScanFailure = (error) => {
            // ignore scan failures, keep scanning
        }

        html5QrcodeScanner = new Html5QrcodeScanner(
            "reader",
            { fps: 10, qrbox: { width: 250, height: 250 } },
            /* verbose= */ false
        )
        html5QrcodeScanner.render(onScanSuccess, onScanFailure)
    }, 100)
}

// ---- 导入 ----
const applyConfig = (data) => {
    // Restore
    if (data.mqtt) {
        localStorage.setItem('mqtt_config', JSON.stringify(data.mqtt))
    }

    if (data.devices) {
        localStorage.setItem('ztc1_devices', JSON.stringify(data.devices))
    }

    if (data.auto_connect) {
        localStorage.setItem('mqtt_auto_connect', data.auto_connect)
    }

    if (data.remarks) {
        Object.keys(data.remarks).forEach(key => {
            localStorage.setItem(key, JSON.stringify(data.remarks[key]))
        })
    }

    clearUrlConfigParam()
    pendingEnc.value = null
    importStatus.value = '配置导入成功！页面即将刷新...'
    setTimeout(() => {
        close()
        emit('config-imported') // Parent can reload or we can reload page
        window.location.reload()
    }, 1500)
}

const validateDevices = (data) => {
    if (!data.devices) return
    if (!Array.isArray(data.devices)) {
        throw new Error('Invalid device configuration')
    }
    data.devices = data.devices.map((device) => {
        if (typeof device === 'string') {
            return { mac: device, version: 'v1' }
        }
        if (!device || typeof device.mac !== 'string') {
            throw new Error('Invalid device configuration')
        }
        if (device.version !== 'v1' && device.version !== 'v2') {
            throw new Error('Invalid device firmware version')
        }
        return device
    })
}

const handleImportPayload = (jsonStr) => {
    try {
        const data = JSON.parse(jsonStr)

        if (localStorage.getItem('mqtt_admin_token')) {
            throw new Error('管理员登录期间不能导入普通 MQTT 用户配置')
        }

        // Basic Validation
        if (!data.mqtt && !data.devices) {
            throw new Error('Invalid Configuration Format')
        }

        validateDevices(data)

        // 加密配置：先要口令解密
        if (data.mqtt && data.mqtt.enc && data.mqtt.enc.d) {
            pendingEnc.value = data
            importPass.value = ''
            importPassError.value = ''
            importStatus.value = ''
            if (!showModal.value) showModal.value = true
            activeTab.value = 'paste'
            return
        }

        applyConfig(data)
    } catch (e) {
        importStatus.value = '导入失败: ' + (e.message || '格式错误')
        console.error(e)
    }
}

const submitImportPass = async () => {
    if (!pendingEnc.value) return
    importPassError.value = ''
    try {
        const creds = await decryptSecret(importPass.value, pendingEnc.value.mqtt.enc)
        const data = pendingEnc.value
        const mqtt = { ...data.mqtt }
        delete mqtt.enc
        if (creds.username) mqtt.username = creds.username
        if (creds.password) mqtt.password = creds.password
        applyConfig({ ...data, mqtt })
    } catch (e) {
        importPassError.value = '口令错误或配置已损坏，请重试'
    }
}

const cancelEncImport = () => {
    pendingEnc.value = null
    importPass.value = ''
    importPassError.value = ''
}

onBeforeUnmount(() => {
    stopScanner()
})

const tryImportFromUrl = () => {
    const params = new URLSearchParams(window.location.search)
    const cfg = params.get('cfg')
    if (!cfg) return
    try {
        const jsonStr = decodeFromBase64Url(cfg)
        handleImportPayload(jsonStr)
    } catch (e) {
        importStatus.value = '导入失败: URL 无效'
        console.error(e)
    }
}

onMounted(() => {
    tryImportFromUrl()
})

const handlePasteImport = () => {
    if(!importText.value) return
    handleImportPayload(importText.value)
}

// Expose open method
defineExpose({
    open: () => {
        isAdmin.value = Boolean(localStorage.getItem('mqtt_admin_token'))
        includePassword.value = false
        encPass.value = ''
        encPass2.value = ''
        exportError.value = ''
        pendingEnc.value = null
        showModal.value = true
        switchTab('export')
    }
})

</script>

<template>
  <Transition name="toast-fade">
    <div v-if="copySuccess" class="copy-toast">✅ 复制成功</div>
  </Transition>
  <div v-if="showModal" class="modal-overlay" @click.self="close">
    <div class="modal-content">
      <div class="modal-header">
        <h3>配置迁移</h3>
        <button class="close-btn" @click="close">×</button>
      </div>

      <div class="tabs">
        <button :class="{ active: activeTab === 'export' }" @click="switchTab('export')">导出二维码</button>
        <button :class="{ active: activeTab === 'scan' }" @click="switchTab('scan')">扫码导入</button>
        <button :class="{ active: activeTab === 'paste' }" @click="switchTab('paste')">文本导入</button>
      </div>

      <div class="tab-content">
        <!-- Export -->
        <div v-if="activeTab === 'export'" class="export-view">
            <p v-if="isAdmin" class="admin-hint">管理员登录期间：导出内容不包含任何密码</p>
            <label v-if="!isAdmin" class="opt-row">
                <input
                    type="checkbox"
                    v-model="includePassword"
                    @change="regenerateExport"
                >
                包含 MQTT 密码（需设置迁移口令加密）
            </label>
            <div v-if="includePassword && !isAdmin" class="pass-box">
                <input
                    type="password"
                    v-model="encPass"
                    placeholder="迁移口令"
                    autocomplete="new-password"
                    @change="regenerateExport"
                >
                <input
                    type="password"
                    v-model="encPass2"
                    placeholder="确认迁移口令"
                    autocomplete="new-password"
                    @change="regenerateExport"
                >
                <p class="field-hint">口令用于加密密码（AES-256-GCM），导入方需输入相同口令才能解密，口令本身不会随码传输</p>
            </div>
            <p v-if="exportError" class="export-error">{{ exportError }}</p>
            <div class="qr-container">
                <canvas ref="qrCanvas"></canvas>
            </div>
            <p>使用其他设备扫描此二维码同步配置</p>
            <textarea readonly v-model="exportText" class="code-area"></textarea>
            <button @click="() => copyToClipboard(exportText)" class="copy-btn">复制文本</button>
            <div class="share-url">
                <p>分享链接 (URL 编码)</p>
                <textarea readonly v-model="shareUrl" class="code-area"></textarea>
                <button @click="() => copyToClipboard(shareUrl)" class="copy-btn">复制链接</button>
            </div>
        </div>

        <!-- Scan -->
        <div v-if="activeTab === 'scan'" class="scan-view">
            <div id="reader" width="300px"></div>
            <p v-if="importStatus">{{ importStatus }}</p>
        </div>

        <!-- Paste -->
        <div v-if="activeTab === 'paste'" class="paste-view">
            <template v-if="!pendingEnc">
                 <textarea v-model="importText" placeholder="在此粘贴配置 JSON..." class="code-area"></textarea>
                 <button @click="handlePasteImport" class="primary-btn">执行导入</button>
                 <p v-if="importStatus" class="status-msg">{{ importStatus }}</p>
            </template>
            <template v-else>
                 <div class="enc-import-box">
                     <p class="enc-title">该配置包含加密的 MQTT 密码</p>
                     <p class="enc-desc">请输入导出时设置的迁移口令进行解密</p>
                     <input
                        type="password"
                        v-model="importPass"
                        placeholder="迁移口令"
                        autocomplete="off"
                        @keyup.enter="submitImportPass"
                     >
                     <p v-if="importPassError" class="export-error">{{ importPassError }}</p>
                     <button @click="submitImportPass" class="primary-btn">解密并导入</button>
                     <button @click="cancelEncImport" class="copy-btn">取消</button>
                 </div>
            </template>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  top: 0; left: 0; right: 0; bottom: 0;
  background: var(--overlay-bg);
  display: flex;
  justify-content: center;
  align-items: center;
  z-index: 999;
}
.modal-content {
  background: var(--bg-card);
  border-radius: 8px;
  width: 90%;
  max-width: 400px;
  max-height: 85vh;
  overflow-y: auto;
  box-shadow: 0 4px 6px var(--shadow-color);
  color: var(--text-primary);
}
.modal-header {
  padding: 15px;
  border-bottom: 1px solid var(--border-light);
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.close-btn {
  background: none;
  border: none;
  font-size: 1.5em;
  cursor: pointer;
  color: var(--text-primary);
}
.tabs {
    display: flex;
    border-bottom: 1px solid var(--border-color);
}
.tabs button {
    flex: 1;
    padding: 10px;
    border: none;
    background: var(--bg-secondary);
    cursor: pointer;
    color: var(--text-primary);
}
.tabs button.active {
    background: var(--bg-card);
    font-weight: bold;
    border-bottom: 2px solid #2196f3;
}
.tab-content {
    padding: 20px;
    text-align: center;
}
.qr-container {
    margin-bottom: 15px;
}
.share-url {
    margin-top: 12px;
}
.code-area {
    width: 100%;
    height: 100px;
    font-size: 0.8em;
    padding: 5px;
    margin-top: 10px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg-input);
    color: var(--text-primary);
}
.scan-view {
    min-height: 300px;
}
.primary-btn, .copy-btn {
    margin-top: 10px;
    padding: 8px 16px;
    background: #2196f3;
    color: white;
    border: none;
    border-radius: 4px;
    cursor: pointer;
}
.copy-btn {
    background: #607d8b;
}
.status-msg {
    color: #4caf50;
    margin-top: 10px;
    font-weight: bold;
}
.copy-toast {
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
.toast-fade-enter-active,
.toast-fade-leave-active {
    transition: opacity 0.3s ease;
}
.toast-fade-enter-from,
.toast-fade-leave-to {
    opacity: 0;
}

/* v1.5.1 密码保护 */
.admin-hint {
    color: #e57373;
    font-size: 0.85em;
    margin: 0 0 10px;
}
.opt-row {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    font-size: 0.9em;
    cursor: pointer;
    margin-bottom: 8px;
}
.pass-box {
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin: 8px 0;
}
.pass-box input {
    padding: 8px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg-input);
    color: var(--text-primary);
}
.field-hint {
    font-size: 0.75em;
    color: var(--text-muted);
    margin: 0;
}
.export-error {
    color: #e53935;
    font-size: 0.85em;
    margin: 6px 0;
}
.enc-import-box {
    display: flex;
    flex-direction: column;
    gap: 10px;
    align-items: stretch;
}
.enc-title {
    font-weight: bold;
    margin: 0;
}
.enc-desc {
    font-size: 0.85em;
    color: var(--text-muted);
    margin: 0;
}
.enc-import-box input {
    padding: 8px;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--bg-input);
    color: var(--text-primary);
}
</style>
