# Biligo 后端接口文档

本文档记录当前后端已经实现的 HTTP API。所有接口默认以 `/api` 为前缀，响应格式为 JSON，除删除接口外均返回响应体。

## 通用约定

- 基础地址：`http://localhost:8080/api`
- 请求体：`Content-Type: application/json`
- 除 `GET /api/health` 与 `POST /api/panel-auth/login` 外，所有接口都需要面板鉴权。
- 面板鉴权请求头：`Authorization: Bearer <token>`。
- 时间字段：字符串，当前后端使用 RFC3339 或前端提交的原始时间字符串。
- 空列表：返回 `[]`。
- 删除成功：返回 `204 No Content`。

错误响应：

```json
{
  "error": "错误信息"
}
```

常见状态码：

| 状态码 | 含义 |
| --- | --- |
| `200` | 请求成功 |
| `201` | 创建成功 |
| `204` | 删除成功，无响应体 |
| `400` | 请求参数错误 |
| `401` | 面板未登录、密码错误或 token 已失效 |
| `404` | 资源不存在 |
| `500` | 服务端错误 |
| `503` | 健康检查失败 |

## 健康检查

### GET `/api/health`

检查后端服务和 SQLite 连接状态。

响应：

```json
{
  "status": "ok",
  "database": "ok",
  "time": "2026-06-12T21:47:14+08:00"
}
```

## 面板鉴权

### POST `/api/panel-auth/login`

使用本地面板密码登录。面板密码来自 `auth.password` 配置；若未配置，服务启动会生成随机密码、写入配置文件并输出到控制台。Token 有效期为 24 小时，服务重启后已签发 Token 失效。

请求：

```json
{
  "password": "panel-password"
}
```

响应：

```json
{
  "token": "random-bearer-token",
  "expiresAt": "2026-06-15T20:00:00+08:00"
}
```

### GET `/api/panel-auth/session`

校验当前 Bearer Token 是否仍有效。该接口需要 `Authorization: Bearer <token>`。

响应：

```json
{
  "expiresAt": "2026-06-15T20:00:00+08:00"
}
```

### POST `/api/panel-auth/logout`

退出当前面板登录，会使当前 Bearer Token 立即失效。该接口需要 `Authorization: Bearer <token>`。

响应：`204 No Content`。

## 账号管理

### GET `/api/auth/session`

返回本地账号配置、Cookie 保存状态和已验证账号数量汇总。该接口不主动请求 Bilibili；真实登录态验证由扫码登录、Cookie 登录或账号验证接口触发。

响应：

```json
{
  "status": "ready",
  "message": "已保存通过登录态验证的账号。",
  "accountCount": 1,
  "configuredAccounts": 1,
  "verifiedAccounts": 1
}
```

字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `status` | string | `ready`、`needs_verify` 或 `missing_account` |
| `message` | string | 给前端展示的状态说明 |
| `accountCount` | number | 本地账号总数 |
| `configuredAccounts` | number | 已保存 Cookie 的账号数 |
| `verifiedAccounts` | number | 最近一次通过登录态验证的账号数 |

## Bilibili 登录

登录功能迁移自 `biliTickerBuy` 的扫码登录和 Cookie 登录思路，只保存用户主动授权后得到的 Cookie，不实现账号密码登录。

### POST `/api/auth/qr/start`

生成 Bilibili 扫码登录二维码。后端调用 Bilibili 二维码生成接口，并返回可直接展示的 PNG Data URL。

请求体：无。

响应：

```json
{
  "ok": true,
  "loginUrl": "https://passport.bilibili.com/h5-app/passport/login/scan?...",
  "qrcodeKey": "xxxxx",
  "qrImageDataUrl": "data:image/png;base64,...",
  "expiresInSeconds": 180,
  "nextAction": "show_qr_and_confirm_scan"
}
```

### POST `/api/auth/qr/poll`

轮询扫码登录状态。扫码确认成功后，后端会从 Bilibili 响应中提取 Cookie，调用登录态验证接口获取用户名，并自动创建一个状态为 `logged_in` 的本地账号。

请求：

```json
{
  "qrcodeKey": "xxxxx",
  "accountName": "主账号",
  "note": "个人使用"
}
```

等待扫码或等待手机确认时的响应：

```json
{
  "ok": true,
  "status": "waiting_scan",
  "message": "二维码未扫码",
  "code": 86101
}
```

登录成功响应：

```json
{
  "ok": true,
  "status": "confirmed",
  "message": "登录成功",
  "code": 0,
  "username": "B站昵称",
  "account": {
    "id": 1,
    "name": "主账号",
    "cookiePreview": "SESSDA...abcdef",
    "hasCookie": true,
    "status": "logged_in",
    "note": "个人使用",
    "createdAt": "2026-06-12T21:00:00+08:00",
    "updatedAt": "2026-06-12T21:00:00+08:00"
  }
}
```

可能的 `status`：

| 状态 | 说明 |
| --- | --- |
| `waiting_scan` | 等待扫码 |
| `waiting_confirm` | 已扫码，等待手机确认 |
| `confirmed` | 登录成功并已保存账号 |
| `expired` | 二维码已过期 |
| `failed` | 登录失败 |

### POST `/api/auth/cookie-login`

用手动填写的 Cookie 验证 Bilibili 登录态。验证成功后自动创建一个状态为 `logged_in` 的本地账号。

请求：

```json
{
  "name": "主账号",
  "cookie": "SESSDATA=xxx; bili_jct=yyy",
  "note": "个人使用"
}
```

验证成功响应：`201 Created`

```json
{
  "ok": true,
  "loggedIn": true,
  "username": "B站昵称",
  "message": "登录态验证成功，账号已保存。",
  "account": {
    "id": 1,
    "name": "主账号",
    "cookiePreview": "SESSDA...abcdef",
    "hasCookie": true,
    "status": "logged_in",
    "note": "个人使用",
    "createdAt": "2026-06-12T21:00:00+08:00",
    "updatedAt": "2026-06-12T21:00:00+08:00"
  }
}
```

验证失败响应：`200 OK`

```json
{
  "ok": false,
  "loggedIn": false,
  "message": "Cookie 未通过 Bilibili 登录态验证。"
}
```

## Bilibili 账号管理

### GET `/api/accounts`

获取本地账号列表。

响应：

```json
[
  {
    "id": 1,
    "name": "主账号",
    "cookiePreview": "SESSDA...abcdef",
    "hasCookie": true,
    "status": "configured",
    "note": "个人使用",
    "createdAt": "2026-06-12T21:00:00+08:00",
    "updatedAt": "2026-06-12T21:00:00+08:00"
  }
]
```

账号状态：

| 状态 | 说明 |
| --- | --- |
| `missing_cookie` | 未保存 Cookie |
| `configured` | 已保存 Cookie，但未验证或重新填写后待验证 |
| `logged_in` | 最近一次登录态验证通过 |
| `login_invalid` | 最近一次登录态验证失败 |

### POST `/api/accounts`

创建账号配置。

请求：

```json
{
  "name": "主账号",
  "cookie": "SESSDATA=xxx; bili_jct=yyy",
  "note": "个人使用"
}
```

响应：`201 Created`，返回创建后的账号对象。

校验：

- `name` 必填。
- `cookie` 可为空；为空时账号状态为 `missing_cookie`。

### PUT `/api/accounts/{id}`

更新账号配置。

请求：

```json
{
  "name": "主账号",
  "cookie": "SESSDATA=xxx; bili_jct=yyy",
  "note": "更新后的备注"
}
```

响应：`200 OK`，返回更新后的账号对象。

说明：

- `name` 必填。
- `cookie` 为空时不会覆盖已有 Cookie，只更新名称和备注。
- `cookie` 非空时会覆盖已有 Cookie，并将账号状态重置为 `configured`。

### GET `/api/accounts/{id}/cookie`

获取指定账号保存的完整 Cookie，用于本地复制。该接口会返回敏感信息，前端列表默认仍只展示脱敏后的 `cookiePreview`。

响应：

```json
{
  "accountId": 1,
  "cookie": "SESSDATA=xxx; bili_jct=yyy",
  "cookiePreview": "SESSDA...abcdef"
}
```

说明：

- 账号未保存 Cookie 时返回 `400`。
- Cookie 只应在本机个人环境中复制和使用，不应分享给他人。

### POST `/api/accounts/{id}/verify`

验证指定账号保存的 Cookie 是否仍然具备 Bilibili 登录态。验证后会把账号状态更新为 `logged_in` 或 `login_invalid`。

响应：

```json
{
  "ok": true,
  "loggedIn": true,
  "accountId": 1,
  "username": "B站昵称",
  "message": "登录态验证成功。",
  "account": {
    "id": 1,
    "name": "主账号",
    "cookiePreview": "SESSDA...abcdef",
    "hasCookie": true,
    "status": "logged_in",
    "note": "个人使用",
    "createdAt": "2026-06-12T21:00:00+08:00",
    "updatedAt": "2026-06-12T21:05:00+08:00"
  }
}
```

### DELETE `/api/accounts/{id}`

删除账号配置。

响应：`204 No Content`。

## 票务信息获取

票务信息获取逻辑迁移自 `biliTickerBuy/interface/project.py`：优先请求新版会员购项目详情接口，失败后回退旧版 `show.bilibili.com` 项目接口，并整理可选择的票信息。

### GET `/api/ticket-projects/history`

获取本地已经获取过的票务项目历史。前端用于项目 ID 输入框的历史下拉。

响应：

```json
[
  {
    "projectId": 123456,
    "projectName": "演出名称",
    "venueName": "场馆名称",
    "venueAddress": "场馆地址",
    "startAt": "2026-06-12 19:30:00",
    "endAt": "2026-06-12 21:30:00",
    "updatedAt": "2026-06-13T10:30:00+08:00"
  }
]
```

### POST `/api/ticket-projects/fetch`

根据抢票项目 ID 或会员购详情页链接获取票务项目和票档信息。成功后会把项目写入历史记录。该接口不读取账号 Cookie，也不返回购票人和收货地址。

请求：

```json
{
  "projectInput": "123456"
}
```

字段说明：

- `projectInput` 必填，可为纯数字项目 ID，也可为包含 `id` 参数的会员购详情页链接。

响应：

```json
{
  "projectId": 123456,
  "projectName": "演出名称",
  "projectUrl": "https://show.bilibili.com/platform/detail.html?id=123456",
  "username": "B站昵称",
  "phone": "",
  "venueName": "场馆名称",
  "venueAddress": "场馆地址",
  "startAt": "2026-06-12 19:30:00",
  "endAt": "2026-06-12 21:30:00",
  "isHotProject": false,
  "hasETicket": true,
  "salesDates": ["2026-06-12"],
  "ticketOptions": [
    {
      "value": "123456:1001:2001:0",
      "display": "2026-06-12 晚场 - VIP - ￥680 - 可购买 - 【起售时间：2026-06-01 12:00:00】",
      "projectId": 123456,
      "screenId": 1001,
      "skuId": 2001,
      "screenName": "2026-06-12 晚场",
      "ticketLevel": "VIP",
      "price": 68000,
      "priceText": "￥680",
      "saleStatus": "可购买",
      "saleStart": "2026-06-01 12:00:00",
      "isHotProject": false,
      "clickable": true
    }
  ]
}
```

说明：`clickable` 来自票档原始字段，回流捡漏模式以 `clickable === true` 作为当前票档可进入下单流程的判断依据

### POST `/api/ticket-projects/account-context`

根据项目 ID 和账号获取该账号下可用的实名购票人、收货地址和账号上下文。该api要求先获取票务信息后再调用。

请求：

```json
{
  "projectInput": "123456",
  "accountId": 1
}
```

字段说明：

- `projectInput` 必填，可为纯数字项目 ID，也可为包含 `id` 参数的会员购详情页链接。
- `accountId` 必填，后端会从本地账号表读取 Cookie，不会在任务响应中暴露 Cookie。

响应：

```json
{
  "projectId": 123456,
  "username": "B站昵称",
  "phone": "",
  "buyers": [
    {
      "id": 1,
      "name": "张三",
      "personalId": "110101199001010000",
      "tel": "13800000000"
    }
  ],
  "addresses": [
    {
      "id": 9,
      "name": "张三",
      "phone": "13800000000",
      "fullAddress": "上海市徐汇区测试路 1 号"
    }
  ]
}
```

## 任务配置及下发

### GET `/api/tasks`

获取任务列表。

响应：

```json
[
  {
    "id": 1,
    "name": "上海场 2 张",
    "accountId": 1,
    "accountName": "主账号",
    "proxyGroupId": 0,
    "proxyGroupName": "",
    "proxyMode": "round_robin",
    "superMode": false,
    "projectId": 123456,
    "projectName": "演出名称",
    "screenId": 1001,
    "skuId": 2001,
    "sessionName": "2026-06-12 晚场",
    "ticketLevel": "VIP",
    "ticketDisplay": "2026-06-12 晚场 - VIP - ￥680 - 可购买 - 【起售时间：2026-06-01 12:00:00】",
    "ticketPrice": 68000,
    "saleStart": "2026-06-01 12:00:00",
    "saleStatus": "可购买",
    "linkId": 0,
    "isHotProject": false,
    "taskMode": "rush",
    "durationMode": "limited",
    "selectedTickets": [],
    "rushDurationSeconds": 600,
    "orderType": 1,
    "payMoney": 136000,
    "buyerInfo": [
      {
        "name": "张三",
        "personalId": "110101199001010000"
      },
      {
        "name": "李四",
        "personalId": "110101199001010001"
      }
    ],
    "buyer": "张三",
    "tel": "13800000000",
    "deliverInfo": {
      "id": 9,
      "name": "张三",
      "phone": "13800000000",
      "fullAddress": "上海市徐汇区测试路 1 号"
    },
    "phone": "",
    "orderId": "",
    "paymentUrl": "",
    "paymentQrImageDataUrl": "",
    "lastCheckedAt": "",
    "timeSyncStrategy": "bilibili",
    "timeOffsetMillis": 0,
    "timeSyncedAt": "",
    "quantity": 2,
    "startAt": "",
    "endAt": "",
    "pollIntervalMillis": 1000,
    "rushPollIntervalMillis": 1000,
    "restockPollIntervalMillis": 1000,
    "status": "draft",
    "lastMessage": "任务已创建，等待下发。",
    "createdAt": "2026-06-12T21:00:00+08:00",
    "updatedAt": "2026-06-12T21:00:00+08:00"
  }
]
```

任务状态：

| 状态 | 说明 |
| --- | --- |
| `draft` | 草稿，已创建但未下发 |
| `waiting_start` | 已下发，等待票档起售时间 |
| `running` | 任务运行中。抢票模式表示已到起售时间并直接尝试订单流程；回流捡漏模式表示正在检测票档状态 |
| `waiting_payment` | 已创建订单，等待用户支付 |
| `succeeded` | 订单接口成功但没有可展示的支付二维码 |
| `duplicate_order` | 检测到重复订单 |
| `paused` | 已停止 |
| `failed` | 任务失败或超时停止 |

### POST `/api/tasks`

创建任务。

请求：

```json
{
  "name": "上海场 2 张",
  "accountId": 1,
  "proxyGroupId": 0,
  "proxyMode": "round_robin",
  "superMode": false,
  "projectId": 123456,
  "projectName": "演出名称",
  "screenId": 1001,
  "skuId": 2001,
  "sessionName": "2026-06-12 晚场",
  "ticketLevel": "VIP",
  "ticketDisplay": "2026-06-12 晚场 - VIP - ￥680 - 可购买 - 【起售时间：2026-06-01 12:00:00】",
  "ticketPrice": 68000,
  "saleStart": "2026-06-01 12:00:00",
  "saleStatus": "可购买",
  "linkId": 0,
  "isHotProject": false,
  "taskMode": "rush",
  "durationMode": "limited",
  "selectedTickets": [],
  "rushDurationSeconds": 600,
  "orderType": 1,
  "payMoney": 136000,
  "buyerInfo": [
    {
      "name": "张三",
      "personalId": "110101199001010000"
    },
    {
      "name": "李四",
      "personalId": "110101199001010001"
    }
  ],
  "buyer": "张三",
  "tel": "13800000000",
  "deliverInfo": {
    "id": 9,
    "name": "张三",
    "phone": "13800000000",
    "fullAddress": "上海市徐汇区测试路 1 号"
  },
  "phone": "",
  "timeSyncStrategy": "bilibili",
  "quantity": 2,
  "startAt": "",
  "endAt": "",
  "pollIntervalMillis": 1000,
  "rushPollIntervalMillis": 1000,
  "restockPollIntervalMillis": 1000
}
```

响应：`201 Created`，返回创建后的任务对象。

校验和默认值：

- `name` 必填。
- `buyerInfo` 非空时，后端将 `quantity` 修正为购票人数。
- `payMoney <= 0` 且 `ticketPrice > 0` 时，后端按票价乘购票人数补齐。
- `pollIntervalMillis <= 0` 时后端默认修正为 `1000`，单位为毫秒。
- `rushPollIntervalMillis` 与 `restockPollIntervalMillis` 仅用于 `taskMode=rush_restock`，分别控制抢票阶段和回流阶段的重试间隔；为空或 `<= 0` 时后端默认按 `pollIntervalMillis` 补齐。
- `timeSyncStrategy` 可选值为 `bilibili` 或 `local`，为空时默认 `bilibili`。
- `taskMode` 可选值为 `rush`、`restock` 或 `rush_restock`，为空时默认 `rush`。
- `proxyGroupId` 仅对 `rush` 和 `rush_restock` 生效；`taskMode=restock` 时后端会自动清空代理组。
- `proxyMode` 可选值为 `round_robin` 或 `concurrent`，为空时默认 `round_robin`；`concurrent` 需要选择代理组。
- `superMode` 仅对 `rush` 和 `rush_restock` 的抢票阶段生效；开启后订单请求会在 `show.bilibili.com`、`www.bilibili.cn`、`www.biligo.com` 三个 base 域名间切换，`createV2` 返回 `412` 或 `3` 时立即切到下一个域名并重新订单准备。
- `durationMode` 可选值为 `limited` 或 `unlimited`，为空时默认 `limited`。
- `rushDurationSeconds <= 0` 时后端默认修正为 `600`；`taskMode=rush_restock` 时该值表示抢票阶段第一次订单请求发出后多少秒再切换回流捡漏。
- `taskMode=restock` 或 `taskMode=rush_restock` 且 `durationMode=limited` 时，下发前需要设置合法 `endAt`；`durationMode=unlimited` 时不需要 `endAt`。
- `taskMode=restock` 或 `taskMode=rush_restock` 时 `selectedTickets` 至少需要 1 个票种；`rush` 和 `rush_restock` 的抢票段仍使用单个 `screenId/skuId/linkId`。
- Web 控制台要求先获取票务信息、选择票信息、购票人和收货地址，再保存任务。

任务模式：

| 模式 | 说明 |
| --- | --- |
| `rush` | 抢票模式。下发后按时间同步策略等待起售时间，起售后直接尝试订单流程。 |
| `restock` | 回流捡漏模式。下发后不等待开票、不进行时间同步，每轮获取一次票务信息，并按最新接口返回顺序检测已选 `selectedTickets`；遇到第一个 `clickable=true` 后提交订单。 |
| `rush_restock` | 抢票+回流捡漏模式。下发后先按抢票模式等待起售并直接尝试订单流程；抢票阶段第一次订单请求发出后 `rushDurationSeconds` 秒仍未进入支付或重复订单时，切换到回流捡漏流程。 |

时间同步策略：

| 策略 | 说明 |
| --- | --- |
| `bilibili` | 默认值。任务下发时请求 `https://api.live.bilibili.com/xlive/open-interface/v1/rtc/getTimestamp` 5 次，按半个 RTT 修正每次样本，去除最大和最小 offset 后取剩余 3 次平均值。 |
| `local` | 使用本地机器时间，`timeOffsetMillis` 为 `0`。 |

### PUT `/api/tasks/{id}`

更新任务配置。

请求字段同创建任务。

响应：`200 OK`，返回更新后的任务对象。

校验和默认值同创建任务。

### DELETE `/api/tasks/{id}`

删除任务。若任务正在运行，后端会先取消运行器；删除成功后会写入一条 `warn` 级别运行日志，并通过 SSE 推送 `log.created` 与 `task.deleted` 事件。

响应：`204 No Content`。

### POST `/api/tasks/{id}/dispatch`

下发任务。行为由 `taskMode` 决定：

- `rush`：后端会先按任务的 `timeSyncStrategy` 同步时间，并写入 `timeOffsetMillis` 与 `timeSyncedAt`；随后启动内置任务运行器，使用“本地时间 + offset”等待票档起售时间。距离起售不足 5 分钟时会拉取一次项目详情校验 `hot_project` 状态，若状态变化会更新任务并按新状态继续等待起售；校验失败会最多重试 10 次，仍失败则继续使用任务本地状态。非并发代理任务距离起售不足 30 秒时会向 `https://show.bilibili.com` 发送 5 个 `HEAD` 请求预热 keep-alive 连接，并保留在同一个 HTTP client 的空闲连接池中供后续订单请求复用。到达起售时间后不再额外检测票档状态，而是直接调用订单准备、订单创建和支付参数接口。
- `restock`：后端不会等待开票、不会进行时间同步和预热，而是立即进入 `running`，按 `pollIntervalMillis` 每轮获取一次票务信息；在最新接口返回顺序中找到第一个属于 `selectedTickets` 且 `clickable=true` 的票种后，先写入任务主票种字段，再复用订单准备、订单创建和支付参数接口。订单准备失败，或同一次准备后的创建订单批次失败后会回到票种检测；订单已创建但支付参数获取失败时继续重试支付参数。`durationMode=limited` 时超过 `endAt` 会停止检测，`durationMode=unlimited` 时持续检测直到用户停止、删除或下单成功。
- `rush_restock`：后端会先按抢票模式同步时间、等待起售、校验 `hot_project` 并预热连接；到达起售时间后直接尝试订单流程。抢票段按 `rushPollIntervalMillis` 重试，截止时间为“抢票阶段第一次订单请求发出时间 + `rushDurationSeconds`”，使用同步后的时间 offset 判断。抢票段成功进入 `waiting_payment` 或检测到重复订单时任务结束；抢票窗口结束仍未成功时写入“抢票窗口结束，切换回流捡漏。”日志，并进入回流捡漏流程。切换后的回流段不再重新时间同步或等待开票，按 `restockPollIntervalMillis` 检测和重试，`durationMode/endAt` 只作用于回流段。

订单阶段采用“1 次 `prepare` 搭配最多 4 次 `createV2`”的重试策略：订单准备成功后，会在同一份准备结果下连续尝试创建订单，任意一次成功即进入支付参数获取；4 次 `createV2` 均失败后才按当前阶段重试间隔等待并重新请求 `prepare`。若 `createV2` 返回 `100034` 且携带新金额，后端会先更新任务金额，再立即重新请求一次 `prepare`，不继续复用旧的准备结果。回流捡漏命中票种后也遵循同一策略，4 次创建订单均失败才回到票种检测列表。纯抢票和纯回流模式继续使用 `pollIntervalMillis`；抢票+回流捡漏模式的抢票阶段使用 `rushPollIntervalMillis`，回流阶段使用 `restockPollIntervalMillis`。成功后进入 `waiting_payment`。

若任务开启 `superMode`，抢票阶段的订单准备、创建订单和获取支付参数会使用当前 SuperMode base 域名；当 `createV2` 返回 `412` 或 `3` 时，后端会按 `show.bilibili.com`、`www.bilibili.cn`、`www.biligo.com` 顺序循环切换到下一个 base 域名，并重新进入订单准备流程。

若任务设置了代理组：

- 抢票模式和抢票+回流模式的抢票阶段会按 `proxyMode` 使用代理组；回流模式和抢票+回流模式的回流阶段不使用代理组。
- `proxyMode=round_robin` 为循环代理：任务使用代理组当前节点，预热连接也会走同一个代理节点；`createV2` 返回 `412` 或 `3` 时立即切换到下一个代理节点重试，其他业务错误继续使用当前节点重试；代理网络请求失败时会标记当前节点检测失败，并切换到下一个代理节点重试。
- `proxyMode=concurrent` 为并发代理：起售前不执行连接预热；每个可用代理节点启动一个抢票 worker，worker 固定使用自己的代理节点，不在线程内切换代理；任一 worker 成功进入 `waiting_payment` 或检测到重复订单后，会停止其他 worker。
- API 代理组会按 `apiConfig.pullBeforeMinutes` 在抢票前指定分钟数拉取快代理私密代理；未配置时默认 5 分钟，若任务下发时已不足该时间、已到起售时间或已过起售时间，则先立即拉取代理节点再抢票；拉取完成日志会输出本次准备好的全部代理节点。

响应：

```json
{
  "id": 1,
  "status": "waiting_start",
  "lastMessage": "任务已下发，等待票档起售时间。"
}
```

实际响应会包含完整任务对象。

### POST `/api/tasks/{id}/pause`

停止任务。若任务正在运行，会取消后端 goroutine，并写入任务日志。接口路径保留 `/pause`，用于兼容既有前端调用。

响应：

```json
{
  "id": 1,
  "status": "paused",
  "lastMessage": "任务已停止。"
}
```

实际响应会包含完整任务对象。

## 代理管理

代理组用于抢票模式和抢票+回流模式的抢票阶段。当前支持普通代理组和快代理私密代理 API 组；任务可通过 `proxyMode` 选择循环代理或并发代理。

代理组对象：

```json
{
  "id": 1,
  "name": "代理组1",
  "type": "api",
  "apiProvider": "kuaidaili_dps",
  "apiConfig": {
    "secretId": "secret-id",
    "secretKey": "secret-key",
    "signType": "hmacsha1",
    "num": "5",
    "pullBeforeMinutes": "5",
    "proxyProtocol": "http"
  },
  "lastPullStatus": "success",
  "lastPullMessage": "已拉取 5 个代理节点。",
  "lastPulledAt": "2026-06-18T20:00:00+08:00",
  "lastTestStatus": "success",
  "lastTestMessage": "检测完成：5/5 个节点可用。",
  "lastTestedAt": "2026-06-18T20:01:00+08:00",
  "nodeCount": 5,
  "availableNodeCount": 5,
  "inUse": false,
  "createdAt": "2026-06-18T19:50:00+08:00",
  "updatedAt": "2026-06-18T20:01:00+08:00"
}
```

代理节点对象：

```json
{
  "id": 1,
  "groupId": 1,
  "name": "节点 1",
  "protocol": "http",
  "host": "127.0.0.1",
  "port": 8080,
  "username": "user",
  "password": "pass",
  "source": "manual",
  "lastTestStatus": "success",
  "lastTestMessage": "代理检测通过。",
  "lastTestLatencyMillis": 86,
  "lastTestIpLocation": "当前 IP：127.0.0.1 来自于：本地",
  "lastTestedAt": "2026-06-18T20:01:00+08:00",
  "createdAt": "2026-06-18T19:50:00+08:00",
  "updatedAt": "2026-06-18T20:01:00+08:00"
}
```

### GET `/api/proxy-groups`

获取全部代理组，包含节点数量、可用节点数量和是否被运行中任务占用。

### POST `/api/proxy-groups`

新增代理组。

请求：

```json
{
  "name": "代理组1",
  "type": "api",
  "apiProvider": "kuaidaili_dps",
  "apiConfig": {
    "secretId": "secret-id",
    "secretKey": "secret-key",
    "signType": "hmacsha1",
    "num": "5",
    "pullBeforeMinutes": "5",
    "proxyProtocol": "http"
  }
}
```

`type=static` 时 `apiProvider` 与 `apiConfig` 可为空；`type=api` 目前仅支持 `apiProvider=kuaidaili_dps`。`pullBeforeMinutes` 表示任务起售前多少分钟自动提取代理，缺省为 `5`。

### PUT `/api/proxy-groups/{id}`

更新代理组。若该代理组正在被 `waiting_start` 或 `running` 任务使用，返回 `400`。

### DELETE `/api/proxy-groups/{id}`

删除代理组及其节点。若该代理组正在被运行中任务使用，返回 `400`。

### GET `/api/proxy-groups/{id}/nodes`

获取代理组下的节点列表。

### POST `/api/proxy-groups/{id}/nodes`

新增手动代理节点。

请求：

```json
{
  "name": "节点 1",
  "protocol": "http",
  "host": "127.0.0.1",
  "port": 8080,
  "username": "user",
  "password": "pass"
}
```

`protocol` 可选 `http`、`https`、`socks5`。账号密码可为空。

仅普通代理组支持手动新增节点；API 代理组需要通过“拉取检测”生成节点。

### PUT `/api/proxy-nodes/{id}`

更新代理节点。若所属代理组正在被运行中任务使用，返回 `400`。

### DELETE `/api/proxy-nodes/{id}`

删除代理节点。若所属代理组正在被运行中任务使用，返回 `400`。

### POST `/api/proxy-groups/{id}/test`

检测组内节点可用性，并写入节点与代理组的最近检测结果。节点延时来自通过代理向 `show.bilibili.com` 发起的 `HEAD` 请求，IP 归属地来自通过同一代理请求 `myip.ipip.net`。

### POST `/api/proxy-groups/{id}/pull-test`

仅 API 代理组可用。后端会从快代理私密代理接口拉取节点、落库为 `source=api`，随后检测节点可用性并返回更新后的代理组。

## 通知管理

通知接口用于在任务成功进入支付前状态时推送提醒。当前支持 `pushplus` 和 `bark`，可同时启用多个接口。

通知对象：

```json
{
  "id": 1,
  "name": "我的手机",
  "provider": "pushplus",
  "config": {
    "token": "pushplus-token"
  },
  "enabled": true,
  "lastTestStatus": "success",
  "lastTestMessage": "测试推送已发送。",
  "lastTestedAt": "2026-06-17T20:00:00+08:00",
  "createdAt": "2026-06-17T19:50:00+08:00",
  "updatedAt": "2026-06-17T20:00:00+08:00"
}
```

### GET `/api/notifications`

获取全部通知接口。

响应：通知对象数组。

### POST `/api/notifications`

新增通知接口。新增后默认未启用。

请求：

```json
{
  "name": "PushPlus",
  "provider": "pushplus",
  "config": {
    "token": "pushplus-token"
  }
}
```

`provider=bark` 时，`config.token` 可以填写 Bark Token，也可以填写完整自建推送地址。

响应：`201 Created`，返回通知对象。

### PUT `/api/notifications/{id}`

更新通知接口名称、类型和配置。

请求体同新增接口。

响应：更新后的通知对象。

### DELETE `/api/notifications/{id}`

删除通知接口。

响应：`204 No Content`。

### POST `/api/notifications/{id}/test`

检测通知接口。后端会真实发送一条测试推送，并将检测结果写入 `lastTestStatus`、`lastTestMessage`、`lastTestedAt`。

响应：更新后的通知对象。发送失败时接口仍返回 `200`，前端根据 `lastTestStatus=error` 展示失败原因。

### POST `/api/notifications/{id}/enable`

启用通知接口。允许多个通知接口同时启用。

响应：更新后的通知对象。

### POST `/api/notifications/{id}/disable`

停用通知接口。

响应：更新后的通知对象。

### 抢票成功推送

当任务成功进入 `waiting_payment` 状态时，后端会向所有已启用通知接口推送“Biligo 抢票成功”。通知正文包含任务名、项目名、票种、数量、订单号和支付提示。

通知发送失败不会改变任务状态；成功或失败结果会写入任务日志，并通过 SSE 的 `log.created` 事件同步给前端。

## 任务日志

### GET `/api/tasks/{id}/logs`

获取指定任务日志，最多返回最近 200 条。

响应：

```json
[
  {
    "id": 2,
    "taskId": 1,
    "level": "info",
    "message": "任务已下发，等待后续监控模块接管。",
    "createdAt": "2026-06-12T21:00:00+08:00"
  }
]
```

### GET `/api/logs`

获取最近任务日志，最多返回 200 条。

可选查询参数：

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `task_id` | number | 指定任务 ID；为空时返回所有任务的最近日志 |

示例：

```text
GET /api/logs?task_id=1
```

响应字段同 `/api/tasks/{id}/logs`。

## 实时事件

### GET `/api/events`

SSE 事件流。连接成功后先推送一次快照，之后推送任务、日志和心跳事件。浏览器原生 `EventSource` 无法设置 `Authorization` 请求头，因此该接口支持通过查询参数传递面板 token：

```text
GET /api/events?token=<token>
```

事件类型：

| 事件 | 说明 |
| --- | --- |
| `snapshot` | 初始任务列表和最近日志 |
| `task.updated` | 任务状态或运行结果更新 |
| `task.deleted` | 任务已删除 |
| `log.created` | 新增任务日志 |
| `heartbeat` | 心跳 |

`snapshot` 数据：

```json
{
  "tasks": [],
  "logs": []
}
```
