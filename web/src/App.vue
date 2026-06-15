<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { ElMessageBox } from 'element-plus'
import {
  Check,
  CirclePlus,
  Close,
  CopyDocument,
  Delete,
  Document,
  Edit,
  Menu as MenuIcon,
  Monitor,
  Refresh,
  Search,
  Setting,
  SwitchButton,
  User,
  VideoPlay,
  View,
} from '@element-plus/icons-vue'
import type {
  Account,
  AccountInput,
  EventSnapshot,
  Health,
  SessionSummary,
  Task,
  TaskInput,
  TaskLog,
  TicketAccountContext,
  TicketAddress,
  TicketBuyer,
  TicketOption,
  TicketProject,
  TicketProjectHistory,
} from './api'
import {
  API_BASE,
  api,
  clearPanelAuthToken,
  getPanelAuthExpiresAt,
  getPanelAuthToken,
  setPanelAuthToken,
  setUnauthorizedHandler,
} from './api'

type SectionKey = 'accounts' | 'taskConfig' | 'taskStatus'
type QRLoginStatus = 'idle' | 'generated' | 'waiting_scan' | 'waiting_confirm' | 'confirmed' | 'expired' | 'failed'
type TicketProjectHistorySuggestion = TicketProjectHistory & { value: string }

const sections: Array<{ key: SectionKey; label: string }> = [
  { key: 'accounts', label: '哔哩哔哩账号管理' },
  { key: 'taskConfig', label: '任务配置' },
  { key: 'taskStatus', label: '任务管理' },
]

const activeSection = ref<SectionKey>('accounts')
const loading = ref(false)
const error = ref('')
const notice = ref('')
const health = ref<Health | null>(null)
const session = ref<SessionSummary | null>(null)
const mobileNavOpen = ref(false)
const panelAuth = reactive({
  checking: true,
  authenticated: false,
  password: '',
  expiresAt: getPanelAuthExpiresAt(),
})

const accounts = ref<Account[]>([])
const ticketProjectHistories = ref<TicketProjectHistory[]>([])
const tasks = ref<Task[]>([])
const logs = ref<TaskLog[]>([])

const editingAccountId = ref<number | null>(null)
const editingTaskId = ref<number | null>(null)

const accountForm = reactive<AccountInput>({
  name: '',
  cookie: '',
  note: '',
})

const qrLogin = reactive({
  qrcodeKey: '',
  qrImageDataUrl: '',
  accountName: '',
  note: '',
  message: '',
  status: 'idle' as QRLoginStatus,
  autoPolling: false,
  polling: false,
  lastCheckedAt: '',
})

const QR_AUTO_POLL_MS = 2500
let qrPollTimer: number | undefined

const taskForm = reactive<TaskInput>({
  name: '',
  accountId: 0,
  projectId: 0,
  projectName: '',
  screenId: 0,
  skuId: 0,
  sessionName: '',
  ticketLevel: '',
  ticketDisplay: '',
  ticketPrice: 0,
  saleStart: '',
  saleStatus: '',
  linkId: 0,
  isHotProject: false,
  orderType: 1,
  payMoney: 0,
  buyerInfo: [],
  buyer: '',
  tel: '',
  deliverInfo: undefined,
  phone: '',
  timeSyncStrategy: 'bilibili',
  quantity: 1,
  startAt: '',
  endAt: '',
  pollIntervalMillis: 1000,
})

const selectedTaskId = ref<number | null>(null)
const ticketProjectInput = ref('')
const fetchedTicketProject = ref<TicketProject | null>(null)
const ticketOptions = ref<TicketOption[]>([])
const ticketBuyers = ref<TicketBuyer[]>([])
const ticketAddresses = ref<TicketAddress[]>([])
const selectedTicketValue = ref('')
const selectedBuyerIndexes = ref<number[]>([])
const selectedAddressId = ref<number>(0)
const nowMs = ref(Date.now())
const sseStatus = ref('未连接')
let eventSource: EventSource | null = null
let clockTimer: number | undefined
let panelAuthExpiryTimer: number | undefined

const selectedTask = computed(() => tasks.value.find((task) => task.id === selectedTaskId.value))
const selectedTaskTicketSubtitle = computed(() =>
  selectedTask.value ? taskTicketSummary(selectedTask.value) : '',
)
const pendingTasks = computed(() => tasks.value.filter((task) => task.status === 'draft' || task.status === 'paused'))
const issuedTasks = computed(() => tasks.value.filter((task) => task.status !== 'draft' && task.status !== 'paused'))
const selectedTicketOption = computed(() =>
  ticketOptions.value.find((ticket) => ticket.value === selectedTicketValue.value),
)
const hasFetchedTicketInfo = computed(() => Boolean(fetchedTicketProject.value?.projectId && ticketOptions.value.length > 0))
const canSaveTask = computed(
  () =>
    taskForm.name.trim() !== '' &&
    taskForm.accountId > 0 &&
    taskForm.ticketDisplay.trim() !== '' &&
    taskForm.skuId > 0 &&
    taskForm.saleStart.trim() !== '' &&
    taskForm.buyerInfo.length > 0 &&
    Boolean(taskForm.deliverInfo?.id) &&
    taskForm.buyer.trim() !== '' &&
    taskForm.tel.trim() !== '' &&
    taskForm.pollIntervalMillis > 0,
)

async function run(action: () => Promise<void>, success?: string) {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    await action()
    if (success) {
      notice.value = success
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function loadAll() {
  await run(async () => {
    health.value = await api.health()

    const [sessionData, accountData, historyData, taskData, logData] = await Promise.all([
      api.session(),
      api.listAccounts(),
      api.listTicketProjectHistory(),
      api.listTasks(),
      api.listLogs(),
    ])
    session.value = sessionData
    accounts.value = accountData ?? []
    ticketProjectHistories.value = historyData ?? []
    tasks.value = taskData ?? []
    logs.value = logData ?? []
  })
}

async function initializePanelAuth() {
  setUnauthorizedHandler(handlePanelUnauthorized)
  panelAuth.checking = true
  try {
    if (!getPanelAuthToken()) {
      panelAuth.authenticated = false
      return
    }
    const session = await api.panelSession()
    panelAuth.expiresAt = session.expiresAt
    panelAuth.authenticated = true
    schedulePanelAuthExpiry(session.expiresAt)
    await bootConsole()
  } catch {
    clearPanelSession()
  } finally {
    panelAuth.checking = false
  }
}

async function loginPanel() {
  await run(async () => {
    const password = panelAuth.password.trim()
    if (!password) {
      throw new Error('请输入面板密码')
    }
    const result = await api.panelLogin({ password })
    if (!result.token) {
      throw new Error('登录响应缺少 token')
    }
    setPanelAuthToken(result.token, result.expiresAt)
    panelAuth.password = ''
    panelAuth.expiresAt = result.expiresAt
    panelAuth.authenticated = true
    schedulePanelAuthExpiry(result.expiresAt)
    await bootConsole()
  }, '面板登录成功')
}

async function bootConsole() {
  await loadAll()
  resetTaskForm()
  connectEvents()
  startClock()
}

function logoutPanel() {
  clearPanelSession()
  notice.value = '已退出面板登录'
}

function handlePanelUnauthorized() {
  clearPanelSession()
  error.value = '面板登录已失效，请重新登录。'
}

function clearPanelSession() {
  clearPanelAuthToken()
  panelAuth.authenticated = false
  panelAuth.expiresAt = ''
  closeEvents()
  stopClock()
  clearPanelAuthExpiry()
  accounts.value = []
  ticketProjectHistories.value = []
  tasks.value = []
  logs.value = []
  session.value = null
}

function schedulePanelAuthExpiry(expiresAt: string) {
  clearPanelAuthExpiry()
  const expiresAtMs = Date.parse(expiresAt)
  if (Number.isNaN(expiresAtMs)) {
    return
  }
  const delay = expiresAtMs - Date.now()
  if (delay <= 0) {
    handlePanelUnauthorized()
    return
  }
  panelAuthExpiryTimer = window.setTimeout(handlePanelUnauthorized, delay)
}

function clearPanelAuthExpiry() {
  if (panelAuthExpiryTimer === undefined) {
    return
  }
  window.clearTimeout(panelAuthExpiryTimer)
  panelAuthExpiryTimer = undefined
}

function resetAccountForm() {
  editingAccountId.value = null
  accountForm.name = ''
  accountForm.cookie = ''
  accountForm.note = ''
}

function resetQRLogin() {
  stopQRAutoPoll()
  qrLogin.qrcodeKey = ''
  qrLogin.qrImageDataUrl = ''
  qrLogin.accountName = ''
  qrLogin.note = ''
  qrLogin.message = ''
  qrLogin.status = 'idle'
  qrLogin.polling = false
  qrLogin.lastCheckedAt = ''
}

async function refreshAccountsAndSession() {
  const [accountData, sessionData] = await Promise.all([api.listAccounts(), api.session()])
  accounts.value = accountData ?? []
  session.value = sessionData
}

function setActiveSection(section: SectionKey) {
  activeSection.value = section
  mobileNavOpen.value = false
}

function sectionIcon(section: SectionKey) {
  const map = {
    accounts: User,
    taskConfig: Setting,
    taskStatus: Monitor,
  }
  return map[section]
}

async function editAccount(account: Account) {
  editingAccountId.value = account.id
  accountForm.name = account.name
  accountForm.cookie = ''
  accountForm.note = account.note
  activeSection.value = 'accounts'
  if (!account.hasCookie) {
    return
  }
  const accountId = account.id
  await run(async () => {
    const result = await api.getAccountCookie(accountId)
    if (editingAccountId.value === accountId) {
      accountForm.cookie = result.cookie
    }
  }, '账号 Cookie 已载入')
}

async function startQRLogin() {
  await run(async () => {
    stopQRAutoPoll()
    const result = await api.startQRLogin()
    qrLogin.qrcodeKey = result.qrcodeKey
    qrLogin.qrImageDataUrl = result.qrImageDataUrl
    qrLogin.status = 'generated'
    qrLogin.message = '二维码已生成，正在等待扫码'
    qrLogin.lastCheckedAt = ''
    startQRAutoPoll()
    void pollQRLoginOnce(true)
  })
}

async function pollQRLogin() {
  await run(() => pollQRLoginOnce(false))
}

function startQRAutoPoll() {
  stopQRAutoPoll()
  qrLogin.autoPolling = true
  qrPollTimer = window.setInterval(() => {
    void pollQRLoginOnce(true)
  }, QR_AUTO_POLL_MS)
}

function stopQRAutoPoll() {
  if (qrPollTimer !== undefined) {
    window.clearInterval(qrPollTimer)
    qrPollTimer = undefined
  }
  qrLogin.autoPolling = false
}

async function pollQRLoginOnce(auto: boolean) {
  if (!qrLogin.qrcodeKey || qrLogin.polling) {
    return
  }

  const pollKey = qrLogin.qrcodeKey
  qrLogin.polling = true
  try {
    const result = await api.pollQRLogin({
      qrcodeKey: pollKey,
      accountName: qrLogin.accountName,
      note: qrLogin.note,
    })
    if (pollKey !== qrLogin.qrcodeKey) {
      return
    }
    qrLogin.status = result.status as QRLoginStatus
    qrLogin.message = result.message
    qrLogin.lastCheckedAt = new Date().toLocaleTimeString()

    if (result.status === 'confirmed') {
      stopQRAutoPoll()
      qrLogin.polling = false
      qrLogin.qrcodeKey = ''
      qrLogin.status = 'confirmed'
      qrLogin.message = '登录成功，已添加账号'
      await refreshAccountsAndSession()
      notice.value = `账号 ${result.username ?? result.account?.name ?? ''} 已登录`.trim()
      return
    }

    if (result.status === 'waiting_scan' || result.status === 'waiting_confirm') {
      if (!auto) {
        notice.value = result.message
      }
      return
    }

    stopQRAutoPoll()
    if (!auto) {
      throw new Error(result.message)
    }
  } catch (err) {
    if (pollKey !== qrLogin.qrcodeKey) {
      return
    }
    stopQRAutoPoll()
    qrLogin.status = 'failed'
    qrLogin.message = err instanceof Error ? err.message : String(err)
    if (!auto) {
      throw err
    }
    error.value = qrLogin.message
  } finally {
    if (pollKey === qrLogin.qrcodeKey) {
      qrLogin.polling = false
    }
  }
}

async function saveAccount() {
  await run(async () => {
    if (editingAccountId.value) {
      await api.updateAccount(editingAccountId.value, accountForm)
    } else {
      await api.createAccount(accountForm)
    }
    resetAccountForm()
    await refreshAccountsAndSession()
  }, '账号信息已保存')
}

async function loginWithCookie() {
  await run(async () => {
    const result = await api.loginWithCookie(accountForm)
    if (!result.loggedIn) {
      throw new Error(result.message)
    }
    resetAccountForm()
    await refreshAccountsAndSession()
  }, 'Cookie 已验证并保存')
}

async function verifyAccount(id: number) {
  await run(async () => {
    const result = await api.verifyAccount(id)
    await refreshAccountsAndSession()
    if (!result.loggedIn) {
      throw new Error(result.message)
    }
  }, '登录态验证成功')
}

async function copyAccountCookie(id: number) {
  await run(async () => {
    const result = await api.getAccountCookie(id)
    await writeClipboardText(result.cookie)
  }, 'Cookie 已复制')
}

async function writeClipboardText(text: string) {
  if (navigator.clipboard && window.isSecureContext) {
    await navigator.clipboard.writeText(text)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  textarea.style.top = '0'
  document.body.appendChild(textarea)
  textarea.select()
  const copied = document.execCommand('copy')
  document.body.removeChild(textarea)
  if (!copied) {
    throw new Error('浏览器拒绝写入剪贴板')
  }
}

async function deleteAccount(id: number) {
  await run(async () => {
    await api.deleteAccount(id)
    await refreshAccountsAndSession()
  }, '账号已删除')
}

function resetTaskForm() {
  editingTaskId.value = null
  resetTicketProjectSelection()
  Object.assign(taskForm, {
    name: '',
    accountId: accounts.value[0]?.id ?? 0,
    projectId: 0,
    projectName: '',
    screenId: 0,
    skuId: 0,
    sessionName: '',
    ticketLevel: '',
    ticketDisplay: '',
    ticketPrice: 0,
    saleStart: '',
    saleStatus: '',
    linkId: 0,
    isHotProject: false,
    orderType: 1,
    payMoney: 0,
    buyerInfo: [],
    buyer: '',
    tel: '',
    deliverInfo: undefined,
    phone: '',
    timeSyncStrategy: 'bilibili',
    quantity: 1,
    startAt: '',
    endAt: '',
    pollIntervalMillis: 1000,
  })
}

function editTask(task: Task) {
  editingTaskId.value = task.id
  Object.assign(taskForm, {
    name: task.name,
    accountId: task.accountId,
    projectId: task.projectId,
    projectName: task.projectName,
    screenId: task.screenId,
    skuId: task.skuId,
    sessionName: task.sessionName,
    ticketLevel: task.ticketLevel,
    ticketDisplay: task.ticketDisplay,
    ticketPrice: task.ticketPrice,
    saleStart: task.saleStart,
    saleStatus: task.saleStatus,
    linkId: task.linkId,
    isHotProject: task.isHotProject,
    orderType: task.orderType,
    payMoney: task.payMoney,
    buyerInfo: task.buyerInfo ?? [],
    buyer: task.buyer,
    tel: task.tel,
    deliverInfo: task.deliverInfo,
    phone: task.phone,
    timeSyncStrategy: task.timeSyncStrategy || 'bilibili',
    quantity: task.quantity,
    startAt: task.startAt,
    endAt: task.endAt,
    pollIntervalMillis: task.pollIntervalMillis || 1000,
  })
  restoreTicketSelectionFromTask(task)
  activeSection.value = 'taskConfig'
}

async function saveTask() {
  await run(async () => {
    if (!taskForm.ticketDisplay || taskForm.skuId <= 0) {
      throw new Error('请先获取票务信息并选择票信息')
    }
    if (taskForm.buyerInfo.length === 0 || !taskForm.deliverInfo?.id) {
      throw new Error('请先选择购票人和收货地址')
    }
    taskForm.quantity = taskForm.buyerInfo.length
    taskForm.payMoney = taskForm.ticketPrice * taskForm.quantity
    if (editingTaskId.value) {
      await api.updateTask(editingTaskId.value, taskForm)
    } else {
      await api.createTask(taskForm)
    }
    resetTaskForm()
    tasks.value = await api.listTasks()
    logs.value = await api.listLogs()
  }, '任务配置已保存')
}

function resetTicketProjectSelection() {
  ticketProjectInput.value = ''
  fetchedTicketProject.value = null
  ticketOptions.value = []
  ticketBuyers.value = []
  ticketAddresses.value = []
  selectedTicketValue.value = ''
  selectedBuyerIndexes.value = []
  selectedAddressId.value = 0
}

function clearSelectedTicketFields() {
  Object.assign(taskForm, {
    projectId: 0,
    projectName: '',
    screenId: 0,
    skuId: 0,
    sessionName: '',
    ticketLevel: '',
    ticketDisplay: '',
    ticketPrice: 0,
    saleStart: '',
    saleStatus: '',
    linkId: 0,
    isHotProject: false,
    payMoney: 0,
    endAt: '',
  })
}

function clearPurchaseFields() {
  selectedBuyerIndexes.value = []
  selectedAddressId.value = 0
  Object.assign(taskForm, {
    buyerInfo: [],
    buyer: '',
    tel: '',
    deliverInfo: undefined,
    phone: '',
    quantity: 1,
    payMoney: 0,
  })
}

function handleTaskAccountChange() {
  ticketBuyers.value = []
  ticketAddresses.value = []
  clearPurchaseFields()
}

function historyOptionLabel(history: TicketProjectHistory) {
  return `${history.projectName || '未命名项目'} · ${history.projectId}`
}

function ticketProjectHistorySuggestions(queryString: string, cb: (items: TicketProjectHistorySuggestion[]) => void) {
  const keyword = queryString.trim().toLowerCase()
  const suggestions = ticketProjectHistories.value
    .filter((history) => {
      if (!keyword) {
        return true
      }
      return (
        String(history.projectId).includes(keyword) ||
        history.projectName.toLowerCase().includes(keyword) ||
        history.venueName.toLowerCase().includes(keyword)
      )
    })
    .map((history) => ({
      ...history,
      value: String(history.projectId),
    }))
  cb(suggestions)
}

function selectTicketProjectHistorySuggestion(history: TicketProjectHistorySuggestion) {
  ticketProjectInput.value = String(history.projectId)
  applyTicketProjectHistory()
}

function applyTicketProjectHistory() {
  const raw = ticketProjectInput.value.trim()
  const history = ticketProjectHistories.value.find((item) => String(item.projectId) === raw)
  ticketOptions.value = []
  selectedTicketValue.value = ''
  clearSelectedTicketFields()
  clearPurchaseFields()
  if (!history) {
    fetchedTicketProject.value = null
    return
  }
  fetchedTicketProject.value = {
    projectId: history.projectId,
    projectName: history.projectName,
    projectUrl: '',
    username: '',
    phone: '',
    venueName: history.venueName,
    venueAddress: history.venueAddress,
    startAt: history.startAt,
    endAt: history.endAt,
    isHotProject: false,
    hasETicket: false,
    salesDates: [],
    ticketOptions: [],
    buyers: [],
    addresses: [],
  }
}

async function fetchTicketProject() {
  await run(async () => {
    const projectInput = ticketProjectInput.value.trim()
    if (!projectInput) {
      throw new Error('请输入抢票项目 ID')
    }
    const previousTicketValue = selectedTicketValue.value
    const project = await api.fetchTicketProject({
      projectInput,
      accountId: 0,
    })
    fetchedTicketProject.value = project
    ticketProjectInput.value = String(project.projectId)
    ticketOptions.value = project.ticketOptions ?? []
    ticketBuyers.value = []
    ticketAddresses.value = []
    clearPurchaseFields()
    if (previousTicketValue && ticketOptions.value.some((ticket) => ticket.value === previousTicketValue)) {
      selectedTicketValue.value = previousTicketValue
      selectTicketOption()
    } else {
      selectedTicketValue.value = ''
      clearSelectedTicketFields()
    }
    ticketProjectHistories.value = await api.listTicketProjectHistory()
  }, '票务信息已获取')
}

async function fetchTicketAccountContext() {
  await run(async () => {
    if (taskForm.accountId <= 0) {
      throw new Error('请先选择账号')
    }
    if (!hasFetchedTicketInfo.value || !fetchedTicketProject.value) {
      throw new Error('请先获取票务信息')
    }

    const context = await api.fetchTicketAccountContext({
      projectInput: String(fetchedTicketProject.value.projectId),
      accountId: taskForm.accountId,
    })
    applyTicketAccountContext(context)
  }, '账号信息已获取')
}

function applyTicketAccountContext(context: TicketAccountContext) {
  ticketBuyers.value = context.buyers ?? []
  ticketAddresses.value = context.addresses ?? []
  clearPurchaseFields()
  taskForm.phone = context.phone ?? ''
}

function selectTicketOption() {
  const ticket = selectedTicketOption.value
  if (!ticket || !fetchedTicketProject.value) {
    clearSelectedTicketFields()
    return
  }
  const defaultEndAt = defaultEndAtFromSaleStart(ticket.saleStart)
  Object.assign(taskForm, {
    projectId: ticket.projectId,
    projectName: fetchedTicketProject.value.projectName,
    screenId: ticket.screenId,
    skuId: ticket.skuId,
    sessionName: ticket.screenName,
    ticketLevel: ticket.ticketLevel,
    ticketDisplay: ticket.display,
    ticketPrice: ticket.price,
    saleStart: ticket.saleStart,
    saleStatus: ticket.saleStatus,
    linkId: ticket.linkId ?? 0,
    isHotProject: ticket.isHotProject,
    payMoney: ticket.price * taskForm.buyerInfo.length,
    endAt: defaultEndAt,
  })
  if (!taskForm.name.trim()) {
    taskForm.name = [fetchedTicketProject.value.projectName, ticket.screenName, ticket.ticketLevel]
      .filter(Boolean)
      .join(' ')
  }
}

function defaultEndAtFromSaleStart(saleStart: string) {
  const parsed = parseTaskTime(saleStart)
  if (!parsed) {
    return ''
  }
  return formatDateTimeInput(new Date(parsed.getTime() + 10 * 60 * 1000))
}

function formatDateTimeInput(date: Date) {
  const pad = (value: number) => String(value).padStart(2, '0')
  return [
    date.getFullYear(),
    pad(date.getMonth() + 1),
    pad(date.getDate()),
  ].join('-') + `T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

function buyerLabel(buyer: TicketBuyer) {
  return `${buyer.name || '未命名购票人'}${buyer.personalId ? ` - ${buyer.personalId}` : ''}`
}

function taskBuyerSummary(task: Task) {
  const buyers = task.buyerInfo ?? []
  if (buyers.length === 0) {
    return '实名购票人：-'
  }
  return `实名购票人：${buyers.map((buyer) => buyerLabel(buyer)).join('、')}`
}

function taskTicketSummary(task: Task) {
  const display = task.ticketDisplay || `${task.sessionName || '-'} / ${task.ticketLevel || '-'}`
  return `${display} / ${task.quantity} 张`
}

function addressLabel(address: TicketAddress) {
  return `${address.name || '未命名地址'} - ${address.phone || '-'} - ${address.fullAddress || address.addr || '-'}`
}

function updateSelectedBuyers() {
  const indexes = selectedBuyerIndexes.value
    .map((item) => Number(item))
    .filter((item) => Number.isInteger(item) && item >= 0 && item < ticketBuyers.value.length)
  selectedBuyerIndexes.value = Array.from(new Set(indexes))
  taskForm.buyerInfo = selectedBuyerIndexes.value.map((index) => ticketBuyers.value[index])
  taskForm.quantity = Math.max(taskForm.buyerInfo.length, 1)
  taskForm.payMoney = taskForm.ticketPrice * taskForm.buyerInfo.length
}

function updateSelectedAddress() {
  const address = ticketAddresses.value.find((item) => item.id === selectedAddressId.value)
  taskForm.deliverInfo = address
  if (address) {
    if (!taskForm.buyer) {
      taskForm.buyer = address.name
    }
    if (!taskForm.tel) {
      taskForm.tel = address.phone
    }
  }
}

function formatMoney(value: number) {
  if (!value) {
    return '￥0'
  }
  return `￥${(value / 100).toFixed(2).replace(/\.?0+$/, '')}`
}

function restoreTicketSelectionFromTask(task: Task) {
  ticketProjectInput.value = task.projectId > 0 ? String(task.projectId) : ''
  fetchedTicketProject.value = task.projectId > 0
    ? {
        projectId: task.projectId,
        projectName: task.projectName,
        projectUrl: '',
        username: '',
        phone: task.phone,
        venueName: '',
        venueAddress: '',
        startAt: '',
        endAt: '',
        isHotProject: task.isHotProject,
        hasETicket: false,
        salesDates: [],
        ticketOptions: [],
        buyers: task.buyerInfo ?? [],
        addresses: task.deliverInfo ? [task.deliverInfo] : [],
      }
    : null

  const restored = task.ticketDisplay
    ? {
        value: `${task.projectId}:${task.screenId}:${task.skuId}:${task.linkId}`,
        display: task.ticketDisplay,
        projectId: task.projectId,
        screenId: task.screenId,
        skuId: task.skuId,
        screenName: task.sessionName,
        ticketLevel: task.ticketLevel,
        price: task.ticketPrice,
        priceText: task.ticketPrice > 0 ? `￥${(task.ticketPrice / 100).toFixed(2).replace(/\.?0+$/, '')}` : '',
        saleStatus: task.saleStatus,
        saleStart: task.saleStart,
        isHotProject: task.isHotProject,
        linkId: task.linkId,
      }
    : null
  ticketOptions.value = restored ? [restored] : []
  selectedTicketValue.value = restored?.value ?? ''
  ticketBuyers.value = task.buyerInfo ?? []
  ticketAddresses.value = task.deliverInfo ? [task.deliverInfo] : []
  selectedBuyerIndexes.value = task.buyerInfo?.map((_, index) => index) ?? []
  selectedAddressId.value = task.deliverInfo?.id ?? 0
}

async function dispatchTask(id: number) {
  await run(async () => {
    await api.dispatchTask(id)
    tasks.value = await api.listTasks()
    logs.value = await api.listLogs(selectedTaskId.value ?? undefined)
  }, '任务已下发')
}

async function startTask(id: number) {
  await run(async () => {
    await api.dispatchTask(id)
    tasks.value = await api.listTasks()
    logs.value = await api.listLogs(selectedTaskId.value ?? undefined)
  }, '任务已启动')
}

async function stopTask(id: number) {
  await run(async () => {
    await api.pauseTask(id)
    tasks.value = await api.listTasks()
    logs.value = await api.listLogs(selectedTaskId.value ?? undefined)
  }, '任务已停止')
}

async function confirmDeleteTask(task: Task) {
  const taskName = task.name.trim() || `#${task.id}`
  try {
    await ElMessageBox.confirm(
      `如果任务正在运行，删除会同时停止后台任务。此操作不可撤销。`,
      `确认删除任务「${taskName}」？`,
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: 'warning',
        confirmButtonClass: 'el-button--danger',
      },
    )
  } catch {
    return
  }
  await deleteTask(task.id)
}

async function deleteTask(id: number) {
  await run(async () => {
    await api.deleteTask(id)
    if (selectedTaskId.value === id) {
      selectedTaskId.value = null
    }
    tasks.value = await api.listTasks()
    logs.value = await api.listLogs()
  }, '任务已删除')
}

async function selectTaskLog(task: Task) {
  selectedTaskId.value = task.id
  await run(async () => {
    logs.value = await api.listLogs(task.id)
  })
}

async function showAllLogs() {
  selectedTaskId.value = null
  await run(async () => {
    logs.value = await api.listLogs()
  })
}

function upsertTask(task: Task) {
  const index = tasks.value.findIndex((item) => item.id === task.id)
  if (index >= 0) {
    tasks.value.splice(index, 1, task)
  } else {
    tasks.value.unshift(task)
  }
}

function addLog(log: TaskLog) {
  if (logs.value.some((item) => item.id === log.id)) {
    return
  }
  if (selectedTaskId.value && log.taskId !== selectedTaskId.value) {
    return
  }
  logs.value = [log, ...logs.value].slice(0, 200)
}

function connectEvents() {
  eventSource?.close()
  const token = getPanelAuthToken()
  if (!token) {
    sseStatus.value = '未登录'
    return
  }
  const source = new EventSource(`${API_BASE}/api/events?token=${encodeURIComponent(token)}`)
  eventSource = source
  sseStatus.value = '连接中'

  source.addEventListener('open', () => {
    sseStatus.value = '已连接'
  })
  source.addEventListener('error', () => {
    sseStatus.value = '重连中'
  })
  source.addEventListener('snapshot', (event) => {
    const snapshot = JSON.parse((event as MessageEvent).data) as EventSnapshot
    tasks.value = snapshot.tasks ?? []
    logs.value = snapshot.logs ?? []
  })
  source.addEventListener('task.updated', (event) => {
    upsertTask(JSON.parse((event as MessageEvent).data) as Task)
  })
  source.addEventListener('task.deleted', (event) => {
    const payload = JSON.parse((event as MessageEvent).data) as { id: number }
    tasks.value = tasks.value.filter((task) => task.id !== payload.id)
    if (selectedTaskId.value === payload.id) {
      selectedTaskId.value = null
    }
  })
  source.addEventListener('log.created', (event) => {
    addLog(JSON.parse((event as MessageEvent).data) as TaskLog)
  })
}

function closeEvents() {
  eventSource?.close()
  eventSource = null
  sseStatus.value = '未连接'
}

function startClock() {
  if (clockTimer !== undefined) {
    return
  }
  clockTimer = window.setInterval(() => {
    nowMs.value = Date.now()
  }, 1000)
}

function stopClock() {
  if (clockTimer === undefined) {
    return
  }
  window.clearInterval(clockTimer)
  clockTimer = undefined
}

function countdownText(task: Task) {
  const target = parseTaskTime(task.saleStart)
  if (!target) {
    return '-'
  }
  const remaining = target.getTime() - calibratedNowMs(task)
  if (remaining <= 0) {
    return '已到起售时间'
  }
  const totalSeconds = Math.ceil(remaining / 1000)
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  return `${hours}小时${minutes}分${seconds}秒`
}

function parseTaskTime(value: string) {
  if (!value) {
    return null
  }
  const normalized = value.includes('T') ? value : value.replace(' ', 'T')
  const parsed = new Date(normalized)
  if (Number.isNaN(parsed.getTime())) {
    return null
  }
  return parsed
}

function calibratedNowMs(task: Task) {
  return nowMs.value + (task.timeOffsetMillis || 0)
}

function timeSyncStrategyLabel(strategy: string) {
  return strategy === 'local' ? '本地时间' : '哔哩哔哩时间'
}

function timeSyncSummary(task: Task) {
  if (!task.timeSyncedAt) {
    return `${timeSyncStrategyLabel(task.timeSyncStrategy)} · 未同步`
  }
  const offset = task.timeOffsetMillis || 0
  return `${timeSyncStrategyLabel(task.timeSyncStrategy)} · offset ${offset >= 0 ? '+' : ''}${offset}ms`
}

async function copyPaymentUrl(task: Task) {
  await run(async () => {
    await writeClipboardText(task.paymentUrl)
  }, '支付链接已复制')
}

function statusLabel(status: string) {
  const map: Record<string, string> = {
    draft: '草稿',
    waiting_start: '等待起售',
    waiting_payment: '待支付',
    succeeded: '已成功',
    duplicate_order: '重复订单',
    paused: '已停止',
    running: '运行中',
    failed: '失败',
    waiting_user: '等待用户',
  }
  return map[status] ?? status
}

function accountStatusLabel(account: Account) {
  const map: Record<string, string> = {
    logged_in: '已登录',
    configured: '待验证',
    login_invalid: '登录失效',
    missing_cookie: '缺少 Cookie',
  }
  if (!account.hasCookie) {
    return '缺少 Cookie'
  }
  return map[account.status] ?? account.status
}

function accountStatusClass(account: Account) {
  if (!account.hasCookie) {
    return 'idle'
  }
  if (account.status === 'logged_in') {
    return 'ready'
  }
  if (account.status === 'login_invalid') {
    return 'bad'
  }
  return 'idle'
}

function accountStatusTagType(account: Account) {
  if (!account.hasCookie) {
    return 'warning'
  }
  if (account.status === 'logged_in') {
    return 'success'
  }
  if (account.status === 'login_invalid') {
    return 'danger'
  }
  return 'warning'
}

function taskStatusClass(status: string) {
  if (status === 'waiting_payment' || status === 'succeeded') {
    return 'ready'
  }
  if (status === 'failed' || status === 'waiting_user') {
    return 'bad'
  }
  return 'idle'
}

function taskStatusTagType(status: string) {
  if (status === 'waiting_payment' || status === 'succeeded') {
    return 'success'
  }
  if (status === 'failed' || status === 'waiting_user') {
    return 'danger'
  }
  if (status === 'running' || status === 'waiting_start') {
    return 'warning'
  }
  return 'info'
}

function logLevelTagType(level: string) {
  if (level === 'warn') {
    return 'warning'
  }
  if (level === 'error') {
    return 'danger'
  }
  return 'info'
}

const qrStatusLabel = computed(() => {
  const map: Record<QRLoginStatus, string> = {
    idle: '未生成',
    generated: '已生成',
    waiting_scan: '等待扫码',
    waiting_confirm: '等待确认',
    confirmed: '已添加',
    expired: '已过期',
    failed: '失败',
  }
  return map[qrLogin.status]
})

function qrStatusClass() {
  if (qrLogin.status === 'confirmed') {
    return 'ready'
  }
  if (qrLogin.status === 'expired' || qrLogin.status === 'failed') {
    return 'bad'
  }
  if (qrLogin.status === 'waiting_scan' || qrLogin.status === 'waiting_confirm' || qrLogin.status === 'generated') {
    return 'idle'
  }
  return 'idle'
}

function qrStatusTagType() {
  if (qrLogin.status === 'confirmed') {
    return 'success'
  }
  if (qrLogin.status === 'expired' || qrLogin.status === 'failed') {
    return 'danger'
  }
  return 'warning'
}

onMounted(async () => {
  await initializePanelAuth()
})

onUnmounted(() => {
  stopQRAutoPoll()
  closeEvents()
  stopClock()
})
</script>

<template>
  <section v-if="panelAuth.checking" class="auth-shell">
    <div class="auth-card">
      <p class="eyebrow">Biligo</p>
      <h1>票务控制台</h1>
      <p class="muted">正在校验面板登录状态...</p>
    </div>
  </section>

  <section v-else-if="!panelAuth.authenticated" class="auth-shell">
    <el-form class="auth-card" label-position="top" @submit.prevent="loginPanel">
      <div>
        <p class="eyebrow">Biligo</p>
        <h1>票务控制台</h1>
        <p class="muted">请输入本地面板密码后继续。</p>
      </div>
      <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" />
      <el-form-item required label="面板密码">
        <el-input
          v-model="panelAuth.password"
          type="password"
          show-password
          autocomplete="current-password"
          placeholder="查看控制台或 config.yaml"
          @keyup.enter="loginPanel"
        />
      </el-form-item>
      <el-button native-type="submit" type="primary" :loading="loading">登录</el-button>
    </el-form>
  </section>

  <el-container v-else class="app-shell">
    <div v-if="loading" class="top-progress" aria-label="页面加载中"></div>

    <el-aside width="260px" class="sidebar desktop-sidebar">
      <div>
        <p class="eyebrow">Biligo</p>
        <h1>票务控制台</h1>
      </div>
      <nav class="nav-list">
        <el-button
          v-for="section in sections"
          :key="section.key"
          :icon="sectionIcon(section.key)"
          text
          :class="{ active: activeSection === section.key }"
          @click="setActiveSection(section.key)"
        >
          {{ section.label }}
        </el-button>
      </nav>
      <div class="system-strip">
        <span :class="['dot', health?.status === 'ok' ? 'ok' : 'bad']"></span>
        <span>API {{ health?.status ?? '未连接' }}</span>
      </div>
    </el-aside>

    <el-drawer v-model="mobileNavOpen" title="票务控制台" direction="ltr" size="280px" class="mobile-drawer">
      <nav class="nav-list drawer-nav">
        <el-button
          v-for="section in sections"
          :key="section.key"
          :icon="sectionIcon(section.key)"
          text
          :class="{ active: activeSection === section.key }"
          @click="setActiveSection(section.key)"
        >
          {{ section.label }}
        </el-button>
      </nav>
      <div class="system-strip drawer-system">
        <span :class="['dot', health?.status === 'ok' ? 'ok' : 'bad']"></span>
        <span>API {{ health?.status ?? '未连接' }}</span>
      </div>
    </el-drawer>

    <el-main class="workspace">
      <header class="topbar">
        <el-button class="mobile-menu-button" :icon="MenuIcon" circle @click="mobileNavOpen = true" />
        <div class="topbar-title">
          <p class="eyebrow">本地单用户模式</p>
          <h2>{{ sections.find((section) => section.key === activeSection)?.label }}</h2>
        </div>
        <div class="topbar-actions">
          <el-button :icon="Refresh" :loading="loading" @click="loadAll">刷新</el-button>
          <el-button :icon="SwitchButton" plain @click="logoutPanel">退出</el-button>
        </div>
      </header>

      <el-alert v-if="error" :title="error" type="error" show-icon :closable="false" />
      <el-alert v-if="notice" :title="notice" type="success" show-icon :closable="false" />

      <section v-if="activeSection === 'accounts'" class="content-grid">
        <div class="stack-column">
          <section class="panel qr-panel">
            <div class="panel-heading">
              <h3>扫码登录</h3>
              <el-button :icon="Close" text @click="resetQRLogin">清空</el-button>
            </div>
            <el-form label-position="top">
              <el-form-item label="账号名称">
                <el-input v-model="qrLogin.accountName" placeholder="默认使用 B 站昵称" clearable />
              </el-form-item>
              <el-form-item label="备注">
                <el-input v-model="qrLogin.note" placeholder="可填写实名人或用途备注" clearable />
              </el-form-item>
            </el-form>
            <div class="qr-status-row">
              <el-tag :type="qrStatusTagType()">{{ qrStatusLabel }}</el-tag>
              <span class="muted">{{ qrLogin.autoPolling ? '自动轮询中' : '自动轮询已停止' }}</span>
              <span v-if="qrLogin.lastCheckedAt" class="muted">上次检查 {{ qrLogin.lastCheckedAt }}</span>
            </div>
            <div v-if="qrLogin.qrImageDataUrl" class="qr-preview">
              <img :src="qrLogin.qrImageDataUrl" alt="Bilibili 登录二维码" />
              <span>{{ qrLogin.message }}</span>
            </div>
            <div class="button-row">
              <el-button type="primary" :icon="CirclePlus" :loading="loading || qrLogin.polling" @click="startQRLogin">
                生成二维码
              </el-button>
              <el-button :icon="Check" :disabled="loading || qrLogin.polling || !qrLogin.qrcodeKey" @click="pollQRLogin">
                检查登录
              </el-button>
            </div>
          </section>

          <el-form class="panel form-panel" label-position="top" @submit.prevent="saveAccount">
            <div class="panel-heading">
              <h3>{{ editingAccountId ? '编辑账号' : '新增账号' }}</h3>
              <el-button :icon="Close" text @click="resetAccountForm">清空</el-button>
            </div>
            <el-form-item required label="账号名称">
              <el-input v-model="accountForm.name" placeholder="例如：主账号" clearable />
            </el-form-item>
            <el-form-item label="Cookie">
              <el-input v-model="accountForm.cookie" type="textarea" :rows="5" placeholder="仅保存在本地 SQLite" />
            </el-form-item>
            <el-form-item label="备注">
              <el-input v-model="accountForm.note" placeholder="可填写实名人或用途备注" clearable />
            </el-form-item>
            <div class="button-row">
              <el-button native-type="submit" type="primary" :icon="Document" :loading="loading">保存账号</el-button>
              <el-button :icon="Check" :disabled="loading || !accountForm.cookie" @click="loginWithCookie">验证并保存</el-button>
            </div>
          </el-form>
        </div>

        <section class="panel list-panel">
          <div class="panel-heading">
            <h3>账号列表</h3>
            <span class="muted">{{ session?.message }}</span>
          </div>
          <div class="summary-row">
            <el-tag>账号 {{ session?.accountCount ?? 0 }}</el-tag>
            <el-tag type="warning">已配置 {{ session?.configuredAccounts ?? 0 }}</el-tag>
            <el-tag type="success">已验证 {{ session?.verifiedAccounts ?? 0 }}</el-tag>
          </div>
          <article v-for="account in accounts" :key="account.id" class="item-card">
            <div>
              <h4>{{ account.name }}</h4>
              <p>{{ account.cookiePreview || '未保存 Cookie' }}</p>
              <small>{{ account.note || '无备注' }}</small>
            </div>
            <div class="actions">
              <el-tag :type="accountStatusTagType(account)">{{ accountStatusLabel(account) }}</el-tag>
              <el-button :icon="Check" :disabled="!account.hasCookie || loading" @click="verifyAccount(account.id)">验证</el-button>
              <el-button :icon="CopyDocument" :disabled="!account.hasCookie || loading" @click="copyAccountCookie(account.id)">
                复制 Cookie
              </el-button>
              <el-button :icon="Edit" @click="editAccount(account)">编辑</el-button>
              <el-button type="danger" plain :icon="Delete" @click="deleteAccount(account.id)">删除</el-button>
            </div>
          </article>
          <el-empty v-if="accounts.length === 0" description="暂无账号" />
        </section>
      </section>

      <section v-if="activeSection === 'taskConfig'" class="content-grid">
        <el-form class="panel form-panel" label-position="top" @submit.prevent="saveTask">
          <div class="panel-heading">
            <h3>{{ editingTaskId ? '编辑任务' : '新增任务' }}</h3>
            <el-button :icon="Close" text @click="resetTaskForm">清空</el-button>
          </div>
          <el-form-item required label="任务名称">
            <el-input v-model="taskForm.name" placeholder="例如：上海场 2 张" clearable />
          </el-form-item>
          <el-form-item required label="抢票项目 ID">
            <div class="input-action-row">
              <el-autocomplete
                v-model="ticketProjectInput"
                :fetch-suggestions="ticketProjectHistorySuggestions"
                value-key="value"
                placeholder="项目 ID 或详情页链接"
                clearable
                @change="applyTicketProjectHistory"
                @select="selectTicketProjectHistorySuggestion"
              >
                <template #default="{ item }">
                  <div class="history-suggestion">
                    <strong>{{ item.projectName || '未命名项目' }}</strong>
                    <span>ID {{ item.projectId }}</span>
                    <small>{{ [item.venueName, item.venueAddress].filter(Boolean).join(' ') || '暂无场馆信息' }}</small>
                  </div>
                </template>
              </el-autocomplete>
              <el-button :icon="Search" :loading="loading" :disabled="!ticketProjectInput.trim()" @click="fetchTicketProject">
                获取信息
              </el-button>
            </div>
          </el-form-item>
          <div v-if="fetchedTicketProject" class="ticket-summary">
            <strong>{{ fetchedTicketProject.projectName || '未命名项目' }}</strong>
            <span>ID {{ fetchedTicketProject.projectId }}</span>
            <span v-if="fetchedTicketProject.venueName || fetchedTicketProject.venueAddress">
              {{ [fetchedTicketProject.venueName, fetchedTicketProject.venueAddress].filter(Boolean).join(' ') }}
            </span>
            <span v-if="fetchedTicketProject.startAt || fetchedTicketProject.endAt">
              {{ fetchedTicketProject.startAt || '-' }} 至 {{ fetchedTicketProject.endAt || '-' }}
            </span>
          </div>
          <el-form-item required label="票信息">
            <el-select
              v-model="selectedTicketValue"
              :disabled="ticketOptions.length === 0"
              placeholder="选择票信息"
              filterable
              @change="selectTicketOption"
            >
              <el-option v-for="ticket in ticketOptions" :key="ticket.value" :value="ticket.value" :label="ticket.display" />
            </el-select>
          </el-form-item>
          <div v-if="selectedTicketOption" class="ticket-detail-grid">
            <span>{{ selectedTicketOption.screenName }}</span>
            <span>{{ selectedTicketOption.ticketLevel }}</span>
            <span>{{ selectedTicketOption.priceText }}</span>
            <span>{{ selectedTicketOption.saleStatus }}</span>
          </div>
          <el-form-item required label="账号">
            <div class="input-action-row">
              <el-select v-model="taskForm.accountId" placeholder="未选择" @change="handleTaskAccountChange">
                <el-option :value="0" label="未选择" />
                <el-option v-for="account in accounts" :key="account.id" :value="account.id" :label="account.name" />
              </el-select>
              <el-button
                :icon="User"
                :loading="loading"
                :disabled="taskForm.accountId <= 0 || !hasFetchedTicketInfo"
                @click="fetchTicketAccountContext"
              >
                获取信息
              </el-button>
            </div>
          </el-form-item>
          <el-form-item required label="实名购票人">
            <div class="buyer-check-list" :class="{ disabled: ticketBuyers.length === 0 }">
              <el-checkbox-group v-model="selectedBuyerIndexes" @change="updateSelectedBuyers">
                <el-checkbox v-for="(buyer, index) in ticketBuyers" :key="`${buyer.id ?? buyer.name}-${index}`" :label="index">
                  {{ buyerLabel(buyer) }}
                </el-checkbox>
              </el-checkbox-group>
              <span v-if="ticketBuyers.length === 0" class="muted">请先在账号行获取信息</span>
            </div>
          </el-form-item>
          <el-form-item required label="收货地址">
            <el-select v-model="selectedAddressId" :disabled="ticketAddresses.length === 0" placeholder="未选择" filterable @change="updateSelectedAddress">
              <el-option :value="0" label="未选择" />
              <el-option v-for="address in ticketAddresses" :key="address.id" :value="address.id" :label="addressLabel(address)" />
            </el-select>
          </el-form-item>
          <el-row :gutter="12">
            <el-col :xs="24" :sm="12">
              <el-form-item required label="联系人姓名">
                <el-input v-model="taskForm.buyer" placeholder="用于订单联系人" clearable />
              </el-form-item>
            </el-col>
            <el-col :xs="24" :sm="12">
              <el-form-item required label="联系人电话">
                <el-input v-model="taskForm.tel" placeholder="用于订单联系人" clearable />
              </el-form-item>
            </el-col>
          </el-row>
          <el-form-item label="支付手机号">
            <el-input v-model="taskForm.phone" placeholder="可选" clearable />
          </el-form-item>
          <el-row :gutter="12">
            <el-col :xs="24" :sm="12">
              <el-form-item label="张数">
                <el-input :model-value="String(taskForm.buyerInfo.length || 0)" disabled />
              </el-form-item>
            </el-col>
            <el-col :xs="24" :sm="12">
              <el-form-item label="预计金额">
                <el-input :model-value="formatMoney(taskForm.payMoney)" disabled />
              </el-form-item>
            </el-col>
          </el-row>
          <el-row :gutter="12">
            <el-col :xs="24" :sm="12">
              <el-form-item required label="重试间隔（ms）">
                <el-input-number v-model="taskForm.pollIntervalMillis" :min="1" controls-position="right" class="full-input" />
              </el-form-item>
            </el-col>
            <el-col :xs="24" :sm="12">
              <el-form-item label="时间同步策略">
                <el-select v-model="taskForm.timeSyncStrategy">
                  <el-option value="bilibili" label="哔哩哔哩时间" />
                  <el-option value="local" label="本地时间" />
                </el-select>
              </el-form-item>
            </el-col>
          </el-row>
          <el-alert type="info" show-icon :closable="false" class="form-tip-alert">
            <template #title>
              <strong>时间同步提示</strong>
            </template>
            <div class="tip-copy">
              <span>默认在任务下发时请求哔哩哔哩时间接口同步。</span>
              <span>建议距离开票时间大于 1 分钟时使用，时间过近可切换本地时间。</span>
            </div>
          </el-alert>
          <el-form-item label="订单类型">
            <el-input :model-value="String(taskForm.orderType)" disabled />
          </el-form-item>
          <el-row :gutter="12">
            <el-col :xs="24" :sm="12">
              <el-form-item label="结束时间">
                <el-date-picker
                  v-model="taskForm.endAt"
                  type="datetime"
                  value-format="YYYY-MM-DDTHH:mm"
                  format="YYYY-MM-DD HH:mm"
                  placeholder=""
                  class="full-input"
                />
              </el-form-item>
            </el-col>
            <el-col :xs="24" :sm="12">
              <el-form-item required label="起售时间">
                <el-input :model-value="taskForm.saleStart" disabled />
              </el-form-item>
            </el-col>
          </el-row>
          <p class="field-hint">不填写则默认开票后 10 分钟停止。</p>
          <el-button native-type="submit" type="primary" :icon="Document" :disabled="!canSaveTask" :loading="loading">
            保存任务
          </el-button>
        </el-form>

        <div class="stack-column">
          <section class="panel list-panel">
            <div class="panel-heading">
              <h3>待下发任务</h3>
              <span class="muted">{{ pendingTasks.length }} 个任务</span>
            </div>
            <article v-for="task in pendingTasks" :key="task.id" class="item-card">
              <div>
                <h4>{{ task.name }}</h4>
                <p>{{ task.projectName || '未选择项目' }} · {{ task.accountName || '未选择账号' }}</p>
                <small>{{ taskTicketSummary(task) }}</small>
                <small>{{ taskBuyerSummary(task) }}</small>
              </div>
              <div class="actions">
                <el-tag :type="taskStatusTagType(task.status)">{{ statusLabel(task.status) }}</el-tag>
                <el-button :icon="Edit" @click="editTask(task)">编辑</el-button>
                <el-button type="primary" :icon="VideoPlay" @click="dispatchTask(task.id)">下发</el-button>
                <el-button type="danger" plain :icon="Delete" @click="confirmDeleteTask(task)">删除</el-button>
              </div>
            </article>
            <el-empty v-if="pendingTasks.length === 0" description="暂无待下发任务" />
          </section>

          <section class="panel list-panel">
            <div class="panel-heading">
              <h3>已下发任务</h3>
              <span class="muted">{{ issuedTasks.length }} 个任务</span>
            </div>
            <article v-for="task in issuedTasks" :key="task.id" class="item-card">
              <div>
                <h4>{{ task.name }}</h4>
                <p>{{ task.projectName || '未选择项目' }} · {{ task.accountName || '未选择账号' }}</p>
                <small>{{ taskTicketSummary(task) }}</small>
                <small>{{ taskBuyerSummary(task) }}</small>
              </div>
              <div class="actions">
                <el-tag :type="taskStatusTagType(task.status)">{{ statusLabel(task.status) }}</el-tag>
                <el-button :icon="Edit" disabled title="请先停止任务后再编辑">编辑</el-button>
                <el-button :icon="SwitchButton" @click="stopTask(task.id)">停止</el-button>
                <el-button type="danger" plain :icon="Delete" @click="confirmDeleteTask(task)">删除</el-button>
              </div>
            </article>
            <el-empty v-if="issuedTasks.length === 0" description="暂无已下发任务" />
          </section>
        </div>
      </section>

      <section v-if="activeSection === 'taskStatus'" class="status-layout">
        <section class="panel">
          <div class="panel-heading">
            <h3>任务状态</h3>
            <span class="muted">SSE {{ sseStatus }} · {{ selectedTask?.name || '未选择任务' }}</span>
          </div>
          <el-table :data="tasks" class="desktop-task-table" row-key="id" empty-text="暂无任务">
            <el-table-column label="任务" min-width="220">
              <template #default="{ row }">
                <strong>{{ row.name }}</strong>
                <small>{{ taskBuyerSummary(row) }}</small>
              </template>
            </el-table-column>
            <el-table-column prop="accountName" label="账号" min-width="130" />
            <el-table-column label="状态" width="110">
              <template #default="{ row }">
                <el-tag :type="taskStatusTagType(row.status)">{{ statusLabel(row.status) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="倒计时/时间源" min-width="170">
              <template #default="{ row }">
                <strong>{{ countdownText(row) }}</strong>
                <small>{{ timeSyncSummary(row) }}</small>
              </template>
            </el-table-column>
            <el-table-column prop="lastMessage" label="最近消息" min-width="220" show-overflow-tooltip />
            <el-table-column label="支付" min-width="130">
              <template #default="{ row }">
                <div v-if="row.status === 'waiting_payment' && row.paymentUrl" class="payment-cell">
                  <img v-if="row.paymentQrImageDataUrl" :src="row.paymentQrImageDataUrl" alt="支付二维码" />
                  <el-button size="small" :icon="CopyDocument" @click="copyPaymentUrl(row)">复制链接</el-button>
                </div>
                <span v-else>-</span>
              </template>
            </el-table-column>
            <el-table-column label="操作" width="260" fixed="right">
              <template #default="{ row }">
                <div class="table-actions">
                  <el-button size="small" :icon="View" @click="selectTaskLog(row)">日志</el-button>
                  <el-button size="small" type="primary" :icon="VideoPlay" @click="startTask(row.id)">启动</el-button>
                  <el-button size="small" :icon="SwitchButton" @click="stopTask(row.id)">停止</el-button>
                  <el-button size="small" type="danger" plain :icon="Delete" @click="confirmDeleteTask(row)">删除</el-button>
                </div>
              </template>
            </el-table-column>
          </el-table>
          <div class="mobile-task-list">
            <article v-for="task in tasks" :key="task.id" class="item-card">
              <div>
                <h4>{{ task.name }}</h4>
                <p>{{ task.accountName || '-' }} · {{ countdownText(task) }}</p>
                <small>{{ taskBuyerSummary(task) }}</small>
                <small>{{ timeSyncSummary(task) }}</small>
                <small>{{ task.lastMessage || '-' }}</small>
              </div>
              <div class="actions">
                <el-tag :type="taskStatusTagType(task.status)">{{ statusLabel(task.status) }}</el-tag>
                <el-button :icon="View" @click="selectTaskLog(task)">日志</el-button>
                <el-button type="primary" :icon="VideoPlay" @click="startTask(task.id)">启动</el-button>
                <el-button :icon="SwitchButton" @click="stopTask(task.id)">停止</el-button>
                <el-button type="danger" plain :icon="Delete" @click="confirmDeleteTask(task)">删除</el-button>
              </div>
            </article>
            <el-empty v-if="tasks.length === 0" description="暂无任务" />
          </div>
        </section>

        <section class="panel log-panel">
          <div class="panel-heading">
            <div>
              <h3>运行日志</h3>
              <small v-if="selectedTaskTicketSubtitle" class="muted">{{ selectedTaskTicketSubtitle }}</small>
            </div>
            <el-button text :icon="Document" @click="showAllLogs">全部</el-button>
          </div>
          <article v-for="log in logs" :key="log.id" class="log-line">
            <el-tag :type="logLevelTagType(log.level)" size="small">{{ log.level }}</el-tag>
            <p>{{ log.message }}</p>
            <time>{{ log.createdAt }}</time>
          </article>
          <el-empty v-if="logs.length === 0" description="暂无日志" />
        </section>
      </section>
    </el-main>
  </el-container>
</template>
