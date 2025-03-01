<template>
  <v-app>
    <v-app-bar color="primary">
      <v-app-bar-title>
        🥟聊天室
      </v-app-bar-title>
      <v-chip class="ma" color="secondary" style="padding-left: 16px; padding-right: 16px;">
        {{ user.nickname }} ({{ user.ip }})
      </v-chip>
      <v-chip
          v-if="wsStatus !== 'OPEN'"
          style="background-color: #ffb300; color: black"
          class="ml-2"
      >
        已断开
      </v-chip>
    </v-app-bar>

    <v-main style="height: 0px; display: flex; flex-direction: column; overflow-y: hidden ;">
      <!-- 这里的height: 0px;太有趣了！不采用100vh而是0px，消息列表和输入框可以利用flex正确啊占据空间。
        另外，手机浏览器会将输入框挤到画面外的bug也就此解决了-->
      <div
          ref="messagesContainer"
          class="messages-container"
          @scroll="handleScroll"
      >
        <v-progress-circular
            v-if="isLoading"
            indeterminate
            color="primary"
        ></v-progress-circular>

        <div
            v-for="msg in messages"
            :key="msg.ID"
            :class="['message-bubble', isMyMessage(msg) ? 'my-message' : 'other-message']"
        >
          <div class="message-header">
            <span>{{ msg.Nickname }}</span>
            <span class="message-time">{{ formatTime(msg.Timestamp) }}</span>
          </div>
          <template v-if="msg.Type === 'text'">
            <div class="message-content">{{ msg.Content }}</div>
          </template>
          <template v-else>
            <v-img
                v-if="isImage(msg.Content)"
                :src="getFileUrl(msg.FileID)"
                @click="showImagePreview(getFileUrl(msg.FileID))"
                max-width="200"
                class="my-2"
            ></v-img>
            <div v-else
              style="display: flex; flex-direction: row; align-items: center; gap: 8px"
            >
              <v-btn
                :href="getFileUrl(msg.FileID)"
                download
                color="primary"
                text
                class="mt-2 px-4 py-2"                
              >
                下载
              </v-btn>
              <span style="white-space: normal;word-break: break-word; padding: 4px 4px;">
                {{ msg.Content }}
              </span>
            </div>
            
            
          </template>
        </div>
      </div>

      <!-- Input Area -->
      <div class="input-container">
        <div class="input-row">
          <v-textarea
              v-model="inputText"
              auto-grow
              rows="1"
              placeholder="输入消息..."
              variant="outlined"
              hide-details
              @keydown.enter="handleEnterKey"
          ></v-textarea>
        </div>
        <div class="action-row d-flex align-center">
          <div class="d-flex align-center flex-grow-1">
            <v-btn
                @click="triggerFileInput"
                variant="text"
                color="primary"
                v-if="!isUploading"
                class="upload-btn"
            >
              上传文件
              <input
                  ref="fileInput"
                  type="file"
                  hidden
                  @change="handleFileSelected"
              />
            </v-btn>
            <div v-if="isUploading" class="d-flex align-center flex-grow-1" style="min-width: 120px;">
              <span class="text-caption mr-2 flex-shrink-0">
                {{ uploadedSize }}MB / {{ totalSize }}MB
              </span>
              <v-progress-linear
                  :model-value="uploadProgress"
                  height="20"
                  class="flex-grow-1"
              >
              </v-progress-linear>
            </div>
          </div>

          <!-- 发送按钮 -->
          <v-btn
              color="primary"
              @click="sendTextMessage"
              :disabled="!inputText || wsStatus !== 'OPEN'"
              class="send-btn ml-2"
          >
            发送
          </v-btn>
        </div>
      </div>

      <v-dialog v-model="imageDialog" max-width="90%">
        <v-img
            :src="previewImage"
            :style="{ maxWidth: '100%', maxHeight: '90vh', objectFit: 'contain' }"
            @click="imageDialog = false"
        ></v-img>
      </v-dialog>
    </v-main>
  </v-app>
</template>

<script setup>
import {ref, reactive, onMounted, nextTick, onBeforeUnmount, watch} from 'vue'
import axios from 'axios'

const messagesContainer = ref(null)
const messages = ref([])
const isLoading = ref(false) // 这个变量禁止重复加载新信息
const isDrawing = ref(false) // 这个变量仅用于禁止奇妙的滚动，在加载新信息成功后100ms内设为真
const savedScrollTop = ref(0) // 这个变量仅用于禁止奇妙的滚动，保存滚动位置，以便在加载新信息后恢复
const user = reactive({ip: '', nickname: ''})
const inputText = ref('')
const uploadProgress = ref(null)
const totalSize = ref(0)
const uploadedSize = ref(0)
const imageDialog = ref(false)
const previewImage = ref('')
const fileInput = ref(null)
const isUploading = ref(false)

// WebSocket 相关变量
const ws = ref(null)
const wsStatus = ref('CLOSED')

// 初始化 WebSocket 连接
const initWebSocket = () => {
  ws.value = new WebSocket(`ws://${window.location.host}/ws`)

  // 设置超时
  const timeout = setTimeout(() => {
    ws.value.close();  // 如果连接超时，关闭WebSocket连接
  }, 2000); // 超时时间是2s

  ws.value.onopen = async () => {
    console.log('WebSocket connected')
    wsStatus.value = 'OPEN'
    clearTimeout(timeout);
    await initUser()
    await reloadMessages();
  }

  ws.value.onmessage = (event) => {
    const newMessage = JSON.parse(event.data)
    messages.value.unshift(newMessage)
    scrollToBottom()
  }

  ws.value.onerror = (error) => {
    console.error('WebSocket error:', error)
    wsStatus.value = 'ERROR'
    clearTimeout(timeout);
  }

  ws.value.onclose = (event) => {
    console.log('WebSocket closed:', event.code, event.reason)
    wsStatus.value = 'CLOSED'

    setTimeout(() => {
      initWebSocket()
    }, 100)
  }
}

// 发送消息方法
const safeSend = (data) => {
  if (ws.value?.readyState === WebSocket.OPEN) {
    ws.value.send(JSON.stringify(data))
  } else {
    console.error('WebSocket not ready, message not sent')
  }
}

// 初始化用户信息
const initUser = async () => {
  try {
    const res = await axios.get(`/api/myname`)
    user.ip = res.data.ip
    user.nickname = res.data.nickname
  } catch (err) {
    console.error('Failed to get user info:', err)
  }
}

// 加载历史消息
const loadMessages = async () => {
  const container = messagesContainer.value
  if (!container) return
  savedScrollTop.value = container.scrollTop // 加载新信息时会发生令人不愉快的滚动，记录当前值以强制保持。

  try {
    if (isLoading.value) return // isLoading是这个函数的并发锁
    isLoading.value = true
    const params = messages.value.length > 0 ? {last_id: messages.value[messages.value.length - 1].ID} : {}
    const res = await axios.get(`/api/messages`, {params})
    if (res.data.length) {
      isDrawing.value = true
      const newMessages = res.data
      messages.value.push(...newMessages)
    }
  } catch (err) {
    console.error('加载消息失败:', err)
  } finally {
    setTimeout(() => {
      isLoading.value = false // 110ms内不允许再加载新的（防止某些抖动导致加载多了）
    }, 110)
    setTimeout(() => {
      isDrawing.value = false // 100ms内不允许再滚动
    }, 100)
  }
}

const reloadMessages = async () => {
  while (isLoading.value) { // 等这个互斥锁，确保没有其他加载新信息的线程后进行清空加载操作
    await new Promise((resolve) => {
      const stop = watch(isLoading, (newVal) => {
        if (!newVal) {
          resolve()
          stop() // 清除监听
        }
      })
    })
  }

  messages.value = []
  await loadMessages()
  scrollToBottom()
}

const handleEnterKey = (event) => {
  if (!event.shiftKey) {
    event.preventDefault(); // 阻止默认换行行为
    sendTextMessage();
  }
}

// 发送文本消息
const sendTextMessage = () => {
  if (inputText.value.trim()) {
    safeSend({Content: inputText.value})
    inputText.value = ''
  }
}

// 触发文件选择
const triggerFileInput = () => {
  fileInput.value?.click()
}

// 处理文件选择
const handleFileSelected = async (event) => {
  const selectedFile = event.target.files[0]
  if (!selectedFile) return

  isUploading.value = true
  const formData = new FormData()
  formData.append('file', selectedFile)

  try {
    totalSize.value = (selectedFile.size / 1024 / 1024).toFixed(1)
    uploadProgress.value = 0
    uploadedSize.value = 0
    await axios.post(`/api/upload`, formData, {
      onUploadProgress: (progressEvent) => {
        uploadedSize.value = (progressEvent.loaded / 1024 / 1024).toFixed(1)
        uploadProgress.value = (progressEvent.loaded / progressEvent.total) * 100
      }
    });
  } catch (err) {
    console.error('文件上传失败:', err)
  } finally {
    isUploading.value = false
    event.target.value = ''
  }
}

// 处理滚动加载
const handleScroll = () => {
  const container = messagesContainer.value
  if (!container) return
  if (isDrawing.value) {
    container.scrollTop = savedScrollTop.value
  }
  // 计算滚动位置与容器尺寸
  const {scrollTop, scrollHeight, clientHeight} = container
  const bottomPosition = scrollHeight - (-scrollTop) - clientHeight
  // 当接近底部时加载更多（阈值 40px）
  if (bottomPosition < 40 && !isLoading.value) {
    loadMessages()
  }
}

// 滚动到底部
const scrollToBottom = () => {
  nextTick(() => {
    if (messagesContainer.value) {
      messagesContainer.value.scrollTop = 0
    }
  })
}

const showImagePreview = (url) => {
  previewImage.value = url
  imageDialog.value = true
}

// 生命周期
onMounted(async () => {
  initWebSocket()
})

onBeforeUnmount(() => {
  if (ws.value) {
    ws.value.close()
    ws.value = null
  }
})

// 工具函数
const isMyMessage = (msg) => msg.Nickname === user.nickname
const isImage = (filename) => /\.(jpg|jpeg|png|gif|webp)$/i.test(filename)
const getFileUrl = (fileId) => `/api/files/${fileId}`
const formatTime = (timestamp) => {
  const date = new Date(timestamp);
  const now = new Date();

  // 提取日期各部分
  const y = date.getFullYear();
  const m = date.getMonth() + 1;
  const d = date.getDate();
  const h = String(date.getHours()).padStart(2, '0');
  const min = String(date.getMinutes()).padStart(2, '0');
  const s = String(date.getSeconds()).padStart(2, '0');

  // 判断年份是否不同
  if (y !== now.getFullYear()) {
    return `${y}年${m}月${d}日 ${h}:${min}:${s}`;
  }

  // 判断是否为同一天
  const isSameDay = (d === now.getDate()) && (m === now.getMonth() + 1);
  if (!isSameDay) {
    return `${m}月${d}日 ${h}:${min}:${s}`;
  }

  // 当天显示时分秒
  return `${h}:${min}:${s}`;
};
</script>

<style scoped>
.messages-container {
  flex: 1 1 auto;
  overflow-y: auto;
  padding-left: 8px;
  padding-right: 8px;
  display: flex;
  flex-direction: column-reverse;
}

.message-bubble {
  max-width: 70%;
  margin: 8px;
  padding: 12px;
  border-radius: 10px;
  position: relative;
}

.message-content {
  white-space: pre-line;
}

.my-message {
  background-color: #e1f5fe;
  color: #000;
  margin-left: auto;
}

.other-message {
  background-color: #eeeeee;
  color: #000;
  margin-right: auto;
}

.input-container {
  flex-shrink: 0;
  padding: 16px;
  background: white;
  box-shadow: 0 -2px 8px rgba(0, 0, 0, 0.1);
  width: 100%;
}

.action-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 8px;
}

.send-btn {
  min-width: 80px;
}

.message-header {
  display: flex;
  justify-content: space-between; /* 左右对齐 */
  align-items: center; /* 垂直居中 */
  margin-bottom: 4px; /* 与内容间距 */
}

.message-time {
  font-size: 0.75rem;
  color: #666;
  margin-left: 8px;
  text-align: right;
}

@media (max-width: 600px) {
  .message-bubble {
    max-width: 85%;
  }

  .action-row {
    flex-wrap: wrap;
    gap: 8px;
  }

  .upload-btn, .send-btn {
    flex: 1 1 auto;
  }
}
</style>