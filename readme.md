# Biligo

<p align="center">
  <img src="assets/logo.png" alt="Biligo Logo" width="180"/>
</p>

Biligo 是一个 哔哩哔哩会员购 抢票辅助工具

基于 Go后端API服务 和 Vue3+Element Plus前端服务 构建

## 项目特色
1. 本项目采用网页作为前端控制台, 便于在**无图形界面**的服务器上部署和使用
2. 本项目API接口完全开放，请见`docs/api.md`，可自由调用API实现自动化操作
3. 本项目抢票会提前预热连接，开票时节省TCP层耗时，快人一步
4. 本项目采用计算http 0.5个rtt的时间同步到b站服务器时间，开票时间更精准
5. 本项目有完善的任务日志，便于观测任务运行情况

## 用前需知
1. Biligo 仅用于**个人学习、研究抢票操作**，严禁用于商业用途。
2. 禁止使用本软件从事违法行为，包括但不限于批量抢票、黄牛倒票等行为
3. 使用者应自行确认使用行为**符合平台服务条款**。因使用本项目产生的账号限制、订单失败或其他后果，由使用者自行承担。

## 本地部署

<details>
<summary>展开查看本地部署说明</summary>

以下说明面向已经下载或自行编译好的二进制文件。

### 1. 准备文件

建议将程序和配置文件放在同一个目录：

```text
biligo/
  biligo          # Linux / macOS
  biligo.exe      # Windows
  config.yaml
```

可以从 `config.example.yaml` 复制一份作为 `config.yaml`。如果启动时没有配置文件，程序会自动生成 `config.yaml`，并在控制台输出面板登录密码。

### 2. 修改配置

常用配置如下：

```yaml
server:
  addr: ":8080"

database:
  path: "data/biligo.db"

auth:
  password: ""

logging:
  levels:
    - info
    - warn
    - error
  color: auto
  file:
    enabled: false
    path: "logs/biligo.log"
```

- `server.addr`：服务监听地址和端口，默认 `:8080`。
- `database.path`：本地 SQLite 数据库路径，默认 `data/biligo.db`。
- `auth.password`：面板登录密码。留空时会自动生成并写入配置文件。
- `logging.file.enabled`：是否写入日志文件。

### 3. 启动程序

Linux / macOS 启动：

```bash
chmod +x ./biligo
./biligo -config config.yaml
```

Windows 启动：

```powershell
.\biligo.exe -config config.yaml
```

Windows 版本默认以系统托盘方式后台运行，不显示控制台窗口。可以在托盘菜单中启动、停止、重启服务，打开 Web 控制台，显示或隐藏控制台，以及打开配置目录和日志目录。

启动后请留意控制台或日志输出：

- 若自动生成了面板密码，会输出密码和写入的配置文件路径。
- 若二进制已嵌入前端，会提示 Web 控制台使用嵌入前端资源。
- 若未嵌入前端，会提示仅启用 API 服务。

### 4. 访问面板

默认访问地址：

```text
http://127.0.0.1:8080/
```

首次进入面板时输入 `auth.password` 中配置的密码。登录后即可在 Web 控制台中配置账号、查询票务信息、创建任务并查看运行状态。

如果你只拿到了未嵌入前端的二进制文件，则浏览器页面不会由程序提供，但 `/api` 仍可访问，例如：

```text
http://127.0.0.1:8080/api/health
```

### 5. 停止程序

在终端中按 `Ctrl+C` 停止服务。下次启动时，如果发现上次未结束的运行任务，程序会自动将其停止，避免误恢复运行。

</details>

## Docker 部署

<details>
<summary>展开查看 Docker 部署说明</summary>

本仓库发布的 Docker 镜像默认已嵌入前端页面，容器启动后只需要暴露一个端口，同时提供 Web 控制台和 `/api`。
Release 发布时会同时推送到 Docker Hub 和 GitHub Container Registry，也可使用阿里云镜像源。

### 1. 准备运行目录

建议把配置、数据库和日志挂载到宿主机，方便升级镜像后继续使用：

```bash
mkdir -p data logs
cp config.example.yaml config.yaml
```

如果没有提前准备 `config.yaml`，程序也会在容器内自动生成 `/app/config.yaml`，并在启动日志中输出面板登录密码。

### 2. 选择镜像部署（三选一）

三种部署方式只有镜像地址不同，端口、挂载和环境变量可以保持一致。展开对应镜像源执行即可。

<details>
<summary>Docker Hub 镜像部署</summary>

```bash
IMAGE=fdcs99/biligo:latest
docker pull "$IMAGE"

docker run -d \
  --name biligo \
  -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -e TZ="Asia/Shanghai" \
  "$IMAGE"
```

</details>

<details>
<summary>GitHub Container Registry 镜像部署</summary>

```bash
IMAGE=ghcr.io/fdcs99/biligo:latest
docker pull "$IMAGE"

docker run -d \
  --name biligo \
  -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -e TZ="Asia/Shanghai" \
  "$IMAGE"
```

</details>

<details>
<summary>阿里云镜像部署</summary>

```bash
IMAGE=crpi-rlahqzawqg2sd5in.cn-hangzhou.personal.cr.aliyuncs.com/fdcs99/biligo:latest
docker pull "$IMAGE"

docker run -d \
  --name biligo \
  -p 8080:8080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -e TZ="Asia/Shanghai" \
  "$IMAGE"
```

</details>

如果还没有准备 `config.yaml`，可以把对应部署命令中的这一行删除：

```bash
-v "$PWD/config.yaml:/app/config.yaml" \
```

启动后访问：

```text
http://127.0.0.1:8080/
```

查看启动日志和自动生成的面板密码：

```bash
docker logs -f biligo
```

### 3. 使用环境变量调整参数

```bash
docker run -d \
  --name biligo \
  -p 18080:18080 \
  -v "$PWD/config.yaml:/app/config.yaml" \
  -v "$PWD/data:/app/data" \
  -v "$PWD/logs:/app/logs" \
  -e TZ="Asia/Shanghai" \
  -e BILIGO_ADDR=":18080" \
  -e BILIGO_PANEL_PASSWORD="your-panel-password" \
  "$IMAGE"
```

常用环境变量：

- `BILIGO_ADDR`：监听地址，例如 `:8080`。
- `BILIGO_PANEL_PASSWORD`：面板登录密码。
- `BILIGO_DB`：数据库路径，例如 `/app/data/biligo.db`。
- `BILIGO_LOG_LEVELS`：日志等级，例如 `info,warn,error`，设置为 `none` 可关闭输出。
- `BILIGO_LOG_COLOR`：控制台颜色，支持 `auto`、`always`、`never`。
- `TZ`：容器时区，例如 `Asia/Shanghai`。票务时间计算已固定按东八区解析，设置 `TZ` 主要用于日志和系统时间显示更直观。

### 4. 停止和升级

停止并删除当前容器：

```bash
docker stop biligo
docker rm biligo
```

升级到最新镜像后，使用同样的 `docker run` 命令重新启动即可：

```bash
docker pull "$IMAGE"
docker stop biligo
docker rm biligo
# 重新执行上面的 docker run 命令
```

</details>

## 平台尊重原则
本项目开发者尊重 **哔哩哔哩** 及其会员购票务系统的运营规则、商业权益和公平购票秩序。

Biligo 的目标是作为本地个人学习与研究工具，帮助用户理解票务流程、整理本地配置和观察任务状态，而不是绕过平台规则、规避安全校验或制造不公平竞争。使用本项目时，用户应自行确认相关行为符合平台服务条款、活动购票规则、实名规则及当地法律法规。

本项目不提供、不鼓励也不承诺以下行为：

1. 绕过验证码、人机校验、风控确认、实名确认、支付确认等平台安全机制。
2. 使用批量账号、批量下单、高频异常请求等方式占用平台资源。
3. 将本项目用于黄牛倒票、转售牟利、恶意囤票或其他破坏公平购票秩序的用途。
4. 承诺抢票成功率，或暗示可以稳定规避平台限制。

若平台规则、接口或风控策略发生变化，应优先停止相关自动化能力，并重新评估其合理性与合规性。若本项目存在侵权或不当内容，请联系 535128725@qq.com，我会第一时间处理或下架。

## 开源协议
本项目采用 **GNU Affero General Public License v3.0** 协议开源，SPDX 标识为 `AGPL-3.0-only`。

你可以在 AGPL-3.0 条款下使用、复制、修改和分发本项目。若你修改本项目并通过网络向他人提供服务，应按照 AGPL-3.0 的要求，向该服务的使用者提供对应版本的源代码。

在使用、分发、修改或部署本项目之前，请自行阅读完整协议文本，并确认你的使用方式符合协议要求。

## 特别鸣谢
项目 [biliTickerBuy](https://github.com/mikumifa/biliTickerBuy) 提供抢票相关逻辑.  
项目 [BHYG](https://github.com/ZianTT/BHYG) 提供风控监测相关逻辑.
