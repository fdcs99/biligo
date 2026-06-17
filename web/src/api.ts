export interface Health {
  status: string
  database: string
  time: string
}

export interface SessionSummary {
  status: string
  message: string
  accountCount: number
  configuredAccounts: number
  verifiedAccounts: number
}

export interface Account {
  id: number
  name: string
  cookiePreview: string
  hasCookie: boolean
  status: string
  note: string
  createdAt: string
  updatedAt: string
}

export interface AccountInput {
  name: string
  cookie: string
  note: string
}

export interface AccountCookieResponse {
  accountId: number
  cookie: string
  cookiePreview: string
}

export interface QRLoginStartResponse {
  ok: boolean
  loginUrl: string
  qrcodeKey: string
  qrImageDataUrl: string
  expiresInSeconds: number
  nextAction: string
}

export interface QRLoginPollInput {
  qrcodeKey: string
  accountName: string
  note: string
}

export interface QRLoginPollResponse {
  ok: boolean
  status: string
  message: string
  code?: number
  username?: string
  account?: Account
}

export interface CookieLoginResponse {
  ok: boolean
  loggedIn: boolean
  username?: string
  message: string
  account?: Account
}

export interface AccountVerifyResponse {
  ok: boolean
  loggedIn: boolean
  accountId: number
  username?: string
  message: string
  account?: Account
}

export interface PanelLoginInput {
  password: string
}

export interface PanelAuthResponse {
  token?: string
  expiresAt: string
}

export interface Notification {
  id: number
  name: string
  provider: string
  config: Record<string, string>
  enabled: boolean
  lastTestStatus: string
  lastTestMessage: string
  lastTestedAt: string
  createdAt: string
  updatedAt: string
}

export interface NotificationInput {
  name: string
  provider: string
  config: Record<string, string>
}

export interface TicketProjectHistory {
  projectId: number
  projectName: string
  venueName: string
  venueAddress: string
  startAt: string
  endAt: string
  updatedAt: string
}

export interface TicketProjectFetchInput {
  projectInput: string
  accountId: number
}

export interface TicketAccountContextInput {
  projectInput: string
  accountId: number
}

export interface TicketAccountContext {
  projectId: number
  username: string
  phone: string
  buyers: TicketBuyer[]
  addresses: TicketAddress[]
}

export interface TicketProject {
  projectId: number
  projectName: string
  projectUrl: string
  username: string
  phone: string
  venueName: string
  venueAddress: string
  startAt: string
  endAt: string
  isHotProject: boolean
  hasETicket: boolean
  salesDates: string[]
  ticketOptions: TicketOption[]
  buyers: TicketBuyer[]
  addresses: TicketAddress[]
}

export interface TicketOption {
  value: string
  display: string
  projectId: number
  screenId: number
  skuId: number
  screenName: string
  ticketLevel: string
  price: number
  priceText: string
  saleStatus: string
  saleStart: string
  isHotProject: boolean
  linkId?: number
  clickable: boolean
}

export interface TicketBuyer {
  id?: number
  name: string
  personalId: string
  tel?: string
  raw?: Record<string, unknown>
}

export interface TicketAddress {
  id: number
  name: string
  phone: string
  prov: string
  city: string
  area: string
  addr: string
  fullAddress: string
  raw?: Record<string, unknown>
}

export interface Task {
  id: number
  name: string
  accountId: number
  accountName: string
  projectId: number
  projectName: string
  screenId: number
  skuId: number
  sessionName: string
  ticketLevel: string
  ticketDisplay: string
  ticketPrice: number
  saleStart: string
  saleStatus: string
  linkId: number
  isHotProject: boolean
  taskMode: string
  durationMode: string
  selectedTickets: TicketOption[]
  rushDurationSeconds: number
  orderType: number
  payMoney: number
  buyerInfo: TicketBuyer[]
  buyer: string
  tel: string
  deliverInfo?: TicketAddress
  phone: string
  orderId: string
  paymentUrl: string
  paymentQrImageDataUrl: string
  lastCheckedAt: string
  timeSyncStrategy: string
  timeOffsetMillis: number
  timeSyncedAt: string
  quantity: number
  startAt: string
  endAt: string
  pollIntervalMillis: number
  status: string
  lastMessage: string
  createdAt: string
  updatedAt: string
}

export interface TaskInput {
  name: string
  accountId: number
  projectId: number
  projectName: string
  screenId: number
  skuId: number
  sessionName: string
  ticketLevel: string
  ticketDisplay: string
  ticketPrice: number
  saleStart: string
  saleStatus: string
  linkId: number
  isHotProject: boolean
  taskMode: string
  durationMode: string
  selectedTickets: TicketOption[]
  rushDurationSeconds: number
  orderType: number
  payMoney: number
  buyerInfo: TicketBuyer[]
  buyer: string
  tel: string
  deliverInfo?: TicketAddress
  phone: string
  timeSyncStrategy: string
  quantity: number
  startAt: string
  endAt: string
  pollIntervalMillis: number
}

export interface TaskLog {
  id: number
  taskId: number
  level: string
  message: string
  createdAt: string
}

export interface EventSnapshot {
  tasks: Task[]
  logs: TaskLog[]
}

export const API_BASE = import.meta.env.VITE_API_BASE ?? ''

const PANEL_AUTH_TOKEN_KEY = 'biligo.panelAuth.token'
const PANEL_AUTH_EXPIRES_KEY = 'biligo.panelAuth.expiresAt'

type APIRequestInit = RequestInit & {
  skipUnauthorizedHandler?: boolean
}

let panelAuthToken = localStorage.getItem(PANEL_AUTH_TOKEN_KEY) ?? ''
let unauthorizedHandler: (() => void) | undefined

export function getPanelAuthToken() {
  return panelAuthToken
}

export function setPanelAuthToken(token: string, expiresAt: string) {
  panelAuthToken = token
  localStorage.setItem(PANEL_AUTH_TOKEN_KEY, token)
  localStorage.setItem(PANEL_AUTH_EXPIRES_KEY, expiresAt)
}

export function clearPanelAuthToken() {
  panelAuthToken = ''
  localStorage.removeItem(PANEL_AUTH_TOKEN_KEY)
  localStorage.removeItem(PANEL_AUTH_EXPIRES_KEY)
}

export function getPanelAuthExpiresAt() {
  return localStorage.getItem(PANEL_AUTH_EXPIRES_KEY) ?? ''
}

export function setUnauthorizedHandler(handler: () => void) {
  unauthorizedHandler = handler
}

async function request<T>(path: string, options: APIRequestInit = {}): Promise<T> {
  const { skipUnauthorizedHandler, headers, ...fetchOptions } = options
  const requestHeaders: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(headers as Record<string, string> | undefined),
  }
  if (panelAuthToken) {
    requestHeaders.Authorization = `Bearer ${panelAuthToken}`
  }

  const response = await fetch(`${API_BASE}${path}`, {
    headers: requestHeaders,
    ...fetchOptions,
  })

  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`
    try {
      const data = await response.json()
      if (data?.error) {
        message = data.error
      }
    } catch {
      // Keep the HTTP status message.
    }
    if (response.status === 401 && !skipUnauthorizedHandler) {
      unauthorizedHandler?.()
    }
    throw new Error(message)
  }

  if (response.status === 204) {
    return undefined as T
  }

  return response.json() as Promise<T>
}

export const api = {
  health: () => request<Health>('/api/health'),
  panelLogin: (payload: PanelLoginInput) =>
    request<PanelAuthResponse>('/api/panel-auth/login', {
      method: 'POST',
      body: JSON.stringify(payload),
      skipUnauthorizedHandler: true,
    }),
  panelSession: () => request<PanelAuthResponse>('/api/panel-auth/session', { skipUnauthorizedHandler: true }),
  panelLogout: () => request<void>('/api/panel-auth/logout', { method: 'POST', skipUnauthorizedHandler: true }),
  session: () => request<SessionSummary>('/api/auth/session'),
  startQRLogin: () =>
    request<QRLoginStartResponse>('/api/auth/qr/start', { method: 'POST' }),
  pollQRLogin: (payload: QRLoginPollInput) =>
    request<QRLoginPollResponse>('/api/auth/qr/poll', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  loginWithCookie: (payload: AccountInput) =>
    request<CookieLoginResponse>('/api/auth/cookie-login', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  listAccounts: () => request<Account[]>('/api/accounts'),
  createAccount: (payload: AccountInput) =>
    request<Account>('/api/accounts', { method: 'POST', body: JSON.stringify(payload) }),
  updateAccount: (id: number, payload: AccountInput) =>
    request<Account>(`/api/accounts/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  getAccountCookie: (id: number) => request<AccountCookieResponse>(`/api/accounts/${id}/cookie`),
  verifyAccount: (id: number) =>
    request<AccountVerifyResponse>(`/api/accounts/${id}/verify`, { method: 'POST' }),
  deleteAccount: (id: number) => request<void>(`/api/accounts/${id}`, { method: 'DELETE' }),

  listTicketProjectHistory: () => request<TicketProjectHistory[]>('/api/ticket-projects/history'),
  fetchTicketProject: (payload: TicketProjectFetchInput) =>
    request<TicketProject>('/api/ticket-projects/fetch', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),
  fetchTicketAccountContext: (payload: TicketAccountContextInput) =>
    request<TicketAccountContext>('/api/ticket-projects/account-context', {
      method: 'POST',
      body: JSON.stringify(payload),
    }),

  listTasks: () => request<Task[]>('/api/tasks'),
  createTask: (payload: TaskInput) =>
    request<Task>('/api/tasks', { method: 'POST', body: JSON.stringify(payload) }),
  updateTask: (id: number, payload: TaskInput) =>
    request<Task>(`/api/tasks/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  deleteTask: (id: number) => request<void>(`/api/tasks/${id}`, { method: 'DELETE' }),
  dispatchTask: (id: number) => request<Task>(`/api/tasks/${id}/dispatch`, { method: 'POST' }),
  pauseTask: (id: number) => request<Task>(`/api/tasks/${id}/pause`, { method: 'POST' }),
  listNotifications: () => request<Notification[]>('/api/notifications'),
  createNotification: (payload: NotificationInput) =>
    request<Notification>('/api/notifications', { method: 'POST', body: JSON.stringify(payload) }),
  updateNotification: (id: number, payload: NotificationInput) =>
    request<Notification>(`/api/notifications/${id}`, { method: 'PUT', body: JSON.stringify(payload) }),
  deleteNotification: (id: number) => request<void>(`/api/notifications/${id}`, { method: 'DELETE' }),
  testNotification: (id: number) => request<Notification>(`/api/notifications/${id}/test`, { method: 'POST' }),
  enableNotification: (id: number) => request<Notification>(`/api/notifications/${id}/enable`, { method: 'POST' }),
  disableNotification: (id: number) => request<Notification>(`/api/notifications/${id}/disable`, { method: 'POST' }),
  listLogs: (taskId?: number) =>
    request<TaskLog[]>(taskId ? `/api/logs?task_id=${taskId}` : '/api/logs'),
}
