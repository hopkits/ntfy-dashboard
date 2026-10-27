# ntfy-dashboard 项目说明书（交接版）

| 项目 | 内容 |
| --- | --- |
| 项目名称 | ntfy-dashboard |
| 项目类型 | 自托管 ntfy server 的 Web 管理控制台（独立 Go 单二进制） |
| 当前版本 | **v0.7.2** |
| 文档日期 | 2026-10-26 |
| 代码位置 | `项目仓库根目录（本仓库）` |
| 目标运行环境 | Debian（systemd），与本机 ntfy server 同机部署 |
| 技术栈 | Go 1.27（构建于 1.27.3）、`net/http`、`html/template`、SQLite（modernc 纯 Go 驱动）、YAML |
| 二进制 | `bin/ntfy-dashboard`（Linux/amd64，约 14 MB，CGO-free） |

> 本文面向接手开发与运维人员，覆盖背景、架构、设计决策、配置、部署、测试、运维与已知限制。部署步骤的精简版见 `README.md`。

---

## 1. 项目背景与目标

ntfy 是一个轻量级推送通知服务。自托管部署后，其管理能力主要依赖命令行（`ntfy user`、`ntfy access` 等）和直接编辑 `/etc/ntfy/server.yml`，缺少可视化管理界面，日常运维存在以下痛点：

- 用户、角色、主题权限的查看与调整需要记忆并手动输入 CLI 命令；
- `server.yml` 手工编辑容易写错格式，且修改后容易忘记重启服务；
- 缓存中的频道与消息没有查询入口，清理历史消息只能操作数据库文件；
- 多步骤操作（改配置 → 校验 → 备份 → 重启）缺乏统一入口。

本项目提供一个**独立于 ntfy 的 Go Web 仪表盘**，在保留 ntfy 原生行为的前提下，把上述运维动作集中到带登录认证的界面中。

### 1.1 设计原则

1. **不重复实现 ntfy 的内部逻辑**：能调用官方 CLI 的就调用 CLI；只读数据直接读 SQLite；配置以文本方式安全编辑。
2. **单二进制、零前端构建**：模板与静态资源通过 `go:embed` 内嵌，部署只需一个可执行文件。
3. **破坏性操作必须可控**：所有写操作需要登录 + CSRF；服务名白名单校验；重启可整体禁用。
4. **失败要可恢复**：修改配置前自动备份、保存前校验、原子写入；错误信息回显到界面。
5. **兼容版本差异**：SQLite 表结构运行时探测；CLI 子命令参数位置按实际语法处理。

---

## 2. 功能范围

### 2.1 已实现

| 模块 | 功能 |
| --- | --- |
| 登录认证 | 单管理员账号；bcrypt 密码校验；HMAC 签名会话 cookie；CSRF 双提交校验 |
| 概览 | 消息数、频道数、用户数、缓存大小；最近消息；CLI/缓存健康状态；服务器信息 |
| 服务器配置 | 读取/编辑 `/etc/ntfy/server.yml`；白名单字段；保存前 YAML 校验；自动备份；原子写入；保留注释与自定义键；**留空即删除该键**；`auth-users` 受保护并按 YAML 列表写回 |
| 服务管理 | 查看 ntfy systemd 服务状态；单独的重启按钮；可通过配置禁用重启 |
| 用户管理 | 用户列表与权限展示；添加/删除用户；修改密码/角色；设置主题权限（rw/ro/wo/deny）；重置访问规则 |
| 频道与消息 | 频道列表与统计；消息分页浏览与搜索；删除单条消息；清空频道；同步维护 `stats` 计数 |

### 2.2 明确不支持（边界）

- **不管理 PostgreSQL 消息存储**：ntfy 配置 `database-url` 时，消息缓存位于 PostgreSQL，本项目频道/消息页不可用（会明确提示），用户与配置管理不受影响。
- **不提供消息发布/订阅界面**：只做管理，不做 ntfy 客户端功能。
- **不管理附件文件**：仅展示附件元数据，不提供下载、删除附件文件。
- **不支持多管理员/RBAC**：仅一个管理员账号（配置中定义）。
- **不修改 Docker 容器**：重启功能依赖 systemd；Docker 部署需关闭 `allow-restart`。

---

## 3. 系统架构

### 3.1 组件关系

```
┌──────────┐   HTTPS    ┌───────────────┐   HTTP    ┌──────────────────────────────┐
│  浏览器   │ ─────────> │ Nginx / Caddy │ ────────> │ ntfy-dashboard (:8090)       │
└──────────┘            └───────────────┘           │                              │
                                                     │  web(html/template+css/js)   │
                                                     │  server(路由/认证/CSRF)       │
                                                     │  settings  ntfycli  ntfydb   │
                                                     │  systemd                    │
                                                     └───────┬──────────┬──────────┘
                                                             │          │
                                  exec `ntfy ...` ───────────┘          │ 读写 /etc/ntfy/server.yml
                                                                        │ 读写 cache.db（SQLite）
                                                                        │ systemctl restart ntfy
                                                                        ▼
                                                     ┌──────────────────────────────┐
                                                     │ ntfy server (systemd)        │
                                                     │  server.yml / user.db / cache.db │
                                                     └──────────────────────────────┘
```

### 3.2 请求与数据流

- **登录**：`GET /login` 下发 CSRF cookie → `POST /login` 校验 CSRF → 校验 bcrypt 密码 → 签发 HMAC 会话 cookie。
- **读用户**：`exec ntfy user [--config server.yml] list` → 解析文本为结构化用户/权限。
- **写用户/权限**：`exec ntfy user/access ...`，密码以 `NTFY_PASSWORD_HASH` 环境变量传入（bcrypt 哈希由仪表盘生成）。
- **读频道/消息**：每次请求打开 `cache.db`（短连接），`PRAGMA table_info` 探测列后查询。
- **改配置**：读取 YAML Node → 应用表单变更（空值删除）→ 校验 → 备份 → 临时文件 + rename 原子替换。
- **重启服务**：`exec systemctl restart <service>`，随后 `systemctl is-active` 回显状态。

### 3.3 目录与模块职责

```
.
├── main.go                      # 入口：参数解析、配置加载、密码哈希、优雅关闭
├── internal/
│   ├── config/                  # 仪表盘自身配置（/etc/ntfy/dashboard.yml）
│   ├── ntfycli/                 # ntfy CLI 封装、输出解析、参数拼装
│   ├── ntfydb/                  # cache.db 读取/删除、schema 探测、stats 同步
│   ├── settings/                # server.yml 读写、白名单、受保护键、校验
│   ├── server/                  # HTTP 路由、中间件、认证、各页面处理器
│   │   ├── server.go            # Server 结构、路由注册、中间件、公共渲染
│   │   ├── auth.go              # 会话与 CSRF
│   │   ├── handlers_auth.go     # 登录/登出
│   │   ├── handlers_pages.go    # 概览/配置/频道/消息
│   │   └── handlers_users.go    # 用户/权限
│   ├── systemd/                 # systemctl 封装（重启/状态）
│   └── web/                     # 内嵌模板、CSS、JS
├── configs/
│   ├── dashboard.yml.example    # 仪表盘配置模板（部署到 /etc/ntfy/dashboard.yml）
│   ├── dashboard.yml            # 本地配置副本（已被 .gitignore 忽略，勿提交）
│   └── ntfy-dashboard.service   # systemd 单元
├── bin/ntfy-dashboard           # 构建产物（git 忽略）
├── build.sh / Makefile
├── README.md                    # 部署与使用简明文档
└── PROJECT_HANDBOOK.md          # 本文
```

### 3.4 技术栈与依赖

| 依赖 | 用途 | 说明 |
| --- | --- | --- |
| `gopkg.in/yaml.v3` | 解析/生成 YAML，保留注释 | 使用 `yaml.Node` 原地修改 |
| `golang.org/x/crypto/bcrypt` | 管理员密码与 ntfy 用户密码哈希 | cost 10 |
| `golang.org/x/term` | 交互式读取密码（`-hash-password`） | 非 TTY 时回退按行读取 |
| `modernc.org/sqlite` | 纯 Go SQLite 驱动 | 无需 CGO，便于交叉编译 |

> `go.mod` 声明 `go 1.27.3`。代码使用 Go 1.22+ 的 `ServeMux` 方法与路径通配符语法，建议构建环境 Go ≥ 1.22。

---

## 4. 关键设计决策与踩坑记录

这些是维护时最容易再次踩到的点，务必保留。

### 4.1 为什么用「CLI + SQLite + YAML」混合方案

| 操作 | 方案 | 原因 |
| --- | --- | --- |
| 用户/权限 | 调官方 CLI | ntfy 的哈希、校验、审计逻辑都在 CLI 内，直接写 `user.db` 容易破坏一致性 |
| 频道/消息 | 直接读 SQLite | ntfy 没有查询缓存消息的 CLI/HTTP 接口 |
| server.yml | 直接读写文本 | 需要保留注释、自定义键与格式 |

### 4.2 ntfy CLI 的 `--config` 位置

`--config` 挂在 `user` / `access` **子命令**上，不是全局 flag：

```bash
# 错误：flag provided but not defined: -config
ntfy --config /etc/ntfy/server.yml user list

# 正确
ntfy user --config /etc/ntfy/server.yml list
ntfy access --config /etc/ntfy/server.yml alice
```

实现位于 `internal/ntfycli/ntfycli.go` 的 `buildArgs`：把 `--config` 插到首个参数之后；以 `-` 开头的调用（如 `--version`）不插入。同时注入 `NTFY_CONFIG_FILE` 环境变量兜底。

### 4.3 用户列表中的匿名伪用户

`ntfy user list` 会输出：

```
user * (role: anonymous, tier: none)
```

它不是可管理账号。`User.IsAnonymous()` 统一判定；用户管理页渲染前过滤，概览页的「用户数」也只统计非匿名账号，避免出现「实际 2 个用户、概览显示 3」的偏差。完整 ACL 原文中仍会显示。

### 4.4 保存配置的「空值即删除」语义

旧实现把表单中所有字段（含未设置的空值）写回文件，产生：

```yaml
database-url: ""
listen-https: ""
```

ntfy 会把空值当作有效配置，导致启动失败。现行规则：

- 有值 → 写入/更新；
- 留空 → **删除该键**；
- 文件中遗留的空值白名单键 → 清理；
- 白名单外的自定义键（有值）→ 原样保留；
- 受保护键（`auth-users`）→ 留空保持原值，绝不删除。

> 前端布尔字段提供「未设置」选项（值为空字符串），避免未设置的布尔项被强制写成 `false`。

### 4.5 `auth-users` 必须是 YAML 列表

ntfy 的 `server.yml` 中 `auth-users` 期望 `[]interface{}`。写成字符串会报：

```
Mismatched type for flag 'auth-users'. Expected '[]interface{}' but actual is 'string'
```

实现：

- 页面用英文逗号分隔多个 `user:bcrypt哈希:role` 便于编辑；
- 保存时 `setAuthUsers` 写成 YAML 序列；
- 若文件里是旧的字符串写法，`normalizeAuthUsers` 在保存时自动规范为列表；
- 保存前校验每项格式、角色（`user`/`admin`）、哈希前缀（`$2`），非法则拒绝且不改文件；
- `auth-users` 在 `protectedKeys` 中，不允许删除。

### 4.6 SQLite 兼容策略

`cache.db` 的 `messages` 表随 ntfy 版本演进。`ntfydb.Open` 通过 `PRAGMA table_info(messages)` 探测列：

- 主键：新版本 `mid`，旧版本回退 `CAST(id AS TEXT)`；
- 缺失列用 SQL 字面量补齐（如 `'' AS icon`、`0 AS expires`），扫描位置固定；
- `tags` 以逗号分隔存储；
- 连接串：`file:<path>?mode=rw&_pragma=busy_timeout(5000)`，容忍与 ntfy 并发访问。

### 4.7 删除消息与统计一致性

删除单条/清空频道在事务中执行，并同步：

```sql
UPDATE stats SET value = MAX(0, value + ?) WHERE key = 'messages';
```

`stats` 表不存在时跳过。统计页仍以 `COUNT(*)` 为准，`stats` 同步主要用于 ntfy 自身计数准确。

### 4.8 其他决策

- **消息缓存路径以 server.yml 为准**：只读取 `server.yml` 的 `cache-file`；`dashboard.yml` 的 `ntfy.cache-db` 仅作显式覆盖。两者都未配置时视为**没有消息缓存**（ntfy 默认使用内存缓存），界面明确提示，不再猜测 `/var/lib/ntfy/cache.db` 之类的默认路径。
- **每次请求打开数据库**：避免 ntfy 重启重建文件后句柄失效；管理后台流量低，开销可接受。
- **会话密钥默认由密码哈希派生**：修改密码会使所有会话失效；配置 `auth.secret` 可固定密钥。
- **服务名白名单**：`^[A-Za-z0-9_.@-]{1,128}$`，命令直接传参、不经 shell。

---

## 5. 构建与发布

### 5.1 环境

- Go ≥ 1.22（当前验证版本 1.27.3）
- 无需 CGO、无需 Node.js

### 5.2 构建命令

```bash
make test          # 单元测试
make build         # 输出 bin/ntfy-dashboard
VERSION=v1.3.4 ./build.sh

# 交叉编译示例
GOOS=linux GOARCH=arm64 VERSION=v1.3.4 ./build.sh
```

`build.sh` 使用 `-trimpath -ldflags "-s -w -X main.version=<VERSION>"`，`CGO_ENABLED=0`。

### 5.3 版本注入

`main.version` 由构建参数注入，`ntfy-dashboard -version` 输出。发版时务必在 `VERSION` 中体现。

### 5.4 产物

- `bin/ntfy-dashboard`（Linux/amd64 约 14 MB）
- 配置模板 `configs/dashboard.yml.example`
- systemd 单元 `configs/ntfy-dashboard.service`

---

## 6. 部署指南

### 6.1 前置条件

- 已安装并运行 ntfy server（systemd 服务名默认 `ntfy`）；
- 存在 `/etc/ntfy/server.yml`；
- 能访问 ntfy 的 `user.db` / `cache.db`（位置以 `server.yml` 的 `auth-file` / `cache-file` 为准）；
- 仪表盘运行用户可执行 `ntfy` 与 `systemctl`。

### 6.2 安装步骤

```bash
# 1) 安装二进制
sudo install -m 0755 bin/ntfy-dashboard /usr/local/bin/ntfy-dashboard

# 2) 创建配置
sudo install -m 0600 -D configs/dashboard.yml.example /etc/ntfy/dashboard.yml

# 3) 生成管理员密码哈希并填入 auth.password-hash
ntfy-dashboard -hash-password

# 4) 可选：生成固定会话密钥并填入 auth.secret
ntfy-dashboard -gen-secret

# 5) 安装并启动 systemd 单元
sudo install -m 0644 configs/ntfy-dashboard.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ntfy-dashboard
sudo systemctl status ntfy-dashboard
```

### 6.3 systemd 单元要点

- `ExecStart=/usr/local/bin/ntfy-dashboard -config /etc/ntfy/dashboard.yml`
- 默认 `User=root`（需写 `/etc/ntfy`、数据库，并调用 systemctl）；
- 加固：`ProtectSystem=strict`、`NoNewPrivileges`、`PrivateTmp`、`ProtectHome` 等；
- 可写目录：`ReadWritePaths=/etc/ntfy -/var/lib/ntfy -/var/cache/ntfy`；
- 若在沙箱下重启服务失败，可增加 `ReadWritePaths=/run` 或关闭 `allow-restart`。

### 6.4 反向代理与 TLS

默认监听 `:8090`（所有网卡）。**对外暴露时必须**：

- 防火墙限制来源；或
- 经 Nginx/Caddy 反代并启用 HTTPS，同时设置 `session.secure: true`。

Nginx 示例见 `README.md`。程序在监听非回环地址且 `session.secure=false` 时会打印安全告警。

### 6.5 权限建议

如不使用 root，可创建专用用户并授予：

- `/etc/ntfy/server.yml` 读写（或所属组写权限）；
- `user.db` / `cache.db` 读写；
- 对 systemd 的重启权限（polkit 或 sudo 规则）；
- 调整单元 `User=` / `Group=`。

---

## 7. 配置参考

### 7.1 仪表盘配置 `/etc/ntfy/dashboard.yml`

| 键 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `listen` | string | `:8090` | 监听地址；`:8090` 表示所有网卡 |
| `base-url` | string | 空 | 仪表盘对外 URL（仅用于展示/链接） |
| `auth.username` | string | `admin` | 管理员用户名 |
| `auth.password-hash` | string | 空 | bcrypt 哈希；为空时启动读取 `NTFY_DASH_PASSWORD` |
| `auth.secret` | string | 空 | 会话签名密钥；为空由密码哈希派生 |
| `session.ttl` | duration | `12h` | 会话有效期 |
| `session.cookie-name` | string | `ntfydash_session` | 会话 cookie 名 |
| `session.secure` | bool | `false` | 仅 HTTPS 发送 cookie |
| `ntfy.server-url` | string | 空 | ntfy 对外地址（展示链接用） |
| `ntfy.cli-path` | string | `ntfy` | ntfy 可执行文件路径 |
| `ntfy.data-dir` | string | `/var/lib/ntfy` | 数据目录（推算数据库路径） |
| `ntfy.cache-db` | string | 空 | 可选显式覆盖；默认取 `server.yml` 的 `cache-file`，两者都没有则提示未配置 |
| `ntfy.user-db` | string | 空 | 留空回退 `<data-dir>/user.db` |
| `ntfy.server-config-file` | string | `/etc/ntfy/server.yml` | 被管理的 ntfy 配置 |
| `ntfy.cli-timeout` | duration | `30s` | CLI 调用超时 |
| `ntfy.service-name` | string | `ntfy` | systemd 服务名 |
| `ntfy.systemctl-path` | string | `systemctl` | systemctl 路径 |
| `ntfy.allow-restart` | bool | `true` | 是否允许从界面重启 |
| `ntfy.restart-timeout` | duration | `30s` | 重启与状态查询超时 |

> `configs/dashboard.yml` 是本地副本，已被 `.gitignore` 忽略；**不要提交真实哈希与密钥**。

### 7.2 `server.yml` 管理范围

- 可编辑键定义在 `internal/settings/settings.go` 的 `knownKeys`（当前 46 项，覆盖：基础网络、缓存、认证、附件、限流、SMTP、日志/指标等）。
- 白名单外但文件中已存在的键：只读展示、保存时原样保留。
- 受保护键：`protectedKeys`，当前仅 `auth-users`。语义为「可改、不可删、留空保持原值」。
- 敏感字段（如 `smtp-sender-pass`）：页面掩码显示，留空表示保持不变；原始文件预览仍会显示，需限制后台访问范围。

### 7.3 `auth-users` 格式

页面输入（逗号分隔）：

```
admin:$2a$10$....:admin,bob:$2a$10$....:user
```

保存到文件：

```yaml
auth-users:
  - "admin:$2a$10$....:admin"
  - "bob:$2a$10$....:user"
```

规则：每项三段 `用户名:bcrypt哈希:role`；角色仅 `user`/`admin`；哈希以 `$2` 开头；非法输入拒绝保存。

---

## 8. 使用手册

### 8.1 登录

访问 `http://<服务器IP>:8090`，使用 `auth.username` 与配置的密码登录。登录失败返回 401，CSRF 失效返回 403。

### 8.2 概览

- 统计卡片：消息总数、频道数、用户数、缓存大小；
- 服务器信息：外部地址、`server.yml` 路径、`cache.db` 路径、`listen-http`、ntfy 版本；
- 最近 8 条消息；
- 若缓存或 CLI 不可用，显示明确原因提示，页面不崩溃。

### 8.3 服务器配置

1. 顶部「ntfy 服务状态」卡片：服务名、状态（active/inactive/failed/unknown）、重启按钮；
2. 配置表：仅白名单字段；布尔字段有「未设置」；敏感字段掩码；
3. 「保存配置」：只写有值项，留空删除；自动备份；不重启；
4. 「重启 ntfy 服务」：独立按钮，使配置生效。

### 8.4 用户管理

- 添加用户：用户名（`A-Za-z0-9_.@-`，1–64）、密码（≥8）、角色；
- 修改角色/密码、删除用户；
- 添加访问规则：`everyone` 或指定用户 + 主题（支持 `*`/`?`）+ 权限；
- 重置某用户（可选指定主题）的规则；
- 查看完整 ACL 原文。

> 由 `server.yml` 静态定义的用户（provisioned）在列表中标记为「配置内置」，不允许在此改密码/删除。

### 8.5 频道与消息

- 频道列表：消息数、最后活动、最新消息摘要；
- 消息页：搜索（标题/正文）、分页（每页 50）、删除单条、清空频道；
- 删除操作直接修改 `cache.db` 并同步 `stats`，建议低峰执行。

---

## 9. HTTP 路由

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| GET | `/healthz` | 否 | 健康检查，返回 `{"status":"ok"}` |
| GET | `/login` | 否 | 登录页（下发 CSRF cookie） |
| POST | `/login` | CSRF | 登录 |
| POST | `/logout` | 是 + CSRF | 退出 |
| GET | `/` | 是 | 概览 |
| GET | `/server` | 是 | 服务器配置页 |
| POST | `/server` | 是 + CSRF | 保存配置 |
| POST | `/server/restart` | 是 + CSRF | 重启 ntfy |
| GET | `/users` | 是 | 用户管理 |
| POST | `/users/add` | 是 + CSRF | 添加用户 |
| POST | `/users/role` | 是 + CSRF | 修改角色 |
| POST | `/users/password` | 是 + CSRF | 修改密码 |
| POST | `/users/remove` | 是 + CSRF | 删除用户 |
| POST | `/users/access` | 是 + CSRF | 设置权限 |
| POST | `/users/access/reset` | 是 + CSRF | 重置权限 |
| GET | `/topics` | 是 | 频道列表 |
| GET | `/topics/{topic}` | 是 | 频道消息（`?q=&offset=`） |
| POST | `/topics/{topic}/clear` | 是 + CSRF | 清空频道 |
| POST | `/messages/{mid}/delete` | 是 + CSRF | 删除消息 |
| GET | `/static/*` | 否 | 内嵌静态资源 |

未登录访问受保护页面返回 303 跳转 `/login`。

---

## 10. 数据与存储

### 10.1 读写的文件

| 文件 | 访问方式 | 说明 |
| --- | --- | --- |
| `/etc/ntfy/server.yml` | 读写 | YAML Node 编辑；备份 `.bak.<时间戳>`；原子替换 |
| `cache.db` | 读写 | 浏览/删除消息；自动探测 schema |
| `user.db` / auth-file | 仅通过 ntfy CLI | 不直接写，避免破坏一致性 |
| `/etc/ntfy/dashboard.yml` | 只读 | 仪表盘自身配置 |

### 10.2 `cache.db` 关键表

`messages`（新版典型列）：

```
id, mid, sequence_id, time, event, expires, topic, message, title, priority,
tags, click, icon, actions, attachment_name, attachment_type, attachment_size,
attachment_expires, attachment_url, attachment_deleted, sender, user,
content_type, encoding, published
```

- `published = 0` 为定时消息，话题统计只计 `published = 1`；
- `tags` 逗号分隔；
- `stats` 表保存消息计数，删除时同步。

### 10.3 时间与大小

- 数据库时间为 Unix 秒；
- 页面统一按服务器本地时区显示；
- 大小格式化由模板函数 `fmtSize` 完成。

---

## 11. 安全设计

| 层面 | 措施 |
| --- | --- |
| 密码 | bcrypt（cost 10）；支持环境变量 `NTFY_DASH_PASSWORD`，避免明文写文件 |
| 会话 | HMAC-SHA256 签名 cookie；`HttpOnly`；`SameSite=Lax`；可配 `Secure`；TTL 可配 |
| CSRF | 双提交 cookie（HttpOnly）+ 表单隐藏字段，常量时间比较 |
| 响应头 | CSP、`X-Frame-Options: DENY`、`nosniff`、`Referrer-Policy: no-referrer` |
| 请求体 | POST 限制 1 MB |
| 命令执行 | `exec.CommandContext` 直接传参，不经 shell；服务名/用户名/主题正则白名单；超时控制 |
| 密码传递 | ntfy 用户密码以 `NTFY_PASSWORD_HASH` 传入，避免明文进入子进程环境 |
| 文件写入 | 备份 0600；临时文件 + rename 原子替换；保留原文件权限 |
| 防误删 | 配置留空删除有明确语义；`auth-users` 受保护；删除消息需确认弹窗 |
| 审计 | 关键操作写日志（登录成功/失败、配置保存与备份路径、用户变更、消息清理、服务重启） |

**仍需注意**：

- 仪表盘权限等价于 ntfy 管理员（可改配置、重启服务），必须限制网络访问；
- 原始配置文件预览会显示敏感值（含 `auth-users` 哈希），仅登录管理员可见；
- 无登录失败次数限制（建议在反向代理层加限速）。

---

## 12. 测试与验证

### 12.1 自动化测试

```bash
go test ./...
go vet ./...
gofmt -l .
```

当前 25 个测试，覆盖：

| 包 | 覆盖内容 |
| --- | --- |
| `internal/ntfycli` | `user list` 输出解析（含真实ntfy v2.34.0 输出、匿名用户）、`--config` 参数插入位置、权限/角色校验 |
| `internal/settings` | 读取、更新、注释与自定义键保留、空值删除、`auth-users` 受保护、格式校验、列表规范化、序列解析 |
| `internal/ntfydb` | 话题/消息查询与排序、搜索、标签解析、删除/清空与 stats 同步、缺失文件处理 |
| `internal/systemd` | 服务名校验、状态查询、重启成功/失败、inactive 不算错误 |
| `internal/server` | 回环监听判断（`:8090`/`0.0.0.0`/`[::]`/`127.0.0.1` 等） |

### 12.2 已执行的手工端到端验证

1. 登录流程：未登录 303 → 登录页 200；无 CSRF POST 403；错误密码 401；正确密码 303。
2. 统计与读取：概览显示消息/频道/缓存大小；频道列表与消息页内容正确。
3. 配置保存：留空字段被删除；注释与自定义键保留；备份文件生成且权限 0600；生成文件可被真实 ntfy v2.34.0 正常加载。
4. `auth-users`：留空不删除并规范为 YAML 列表；非法格式被拒绝且文件不变；真实 ntfy 用该配置成功启动。
5. 消息管理：删除单条、清空频道后计数归零，`stats` 同步。
6. 服务管理：状态显示 active；单独重启按钮返回成功；保存与重启已拆分。
7. 真实 CLI 集成（ntfy v2.34.0）：用户列表、添加用户、改角色、设置权限均成功。
8. 缓存路径来源：`server.yml` 配置 `cache-file` 时页面显示该路径并正常读取；未配置时概览与频道页均提示「未配置消息缓存」，不再回退默认路径。
9. 用户统计：真实 `ntfy user list` 含 3 个账号 + 匿名伪用户时，概览「用户数」显示 3，用户管理页同样只列出 3 个真实账号。

### 12.3 交接后回归清单

改动后至少执行：

- [ ] `go test ./...` 全绿
- [ ] `go vet ./...` 无输出
- [ ] 登录/登出、CSRF 拒绝
- [ ] 配置保存：空值删除、`auth-users` 保留且为列表、真实 ntfy 可启动
- [ ] 用户：列表/添加/改角色/权限
- [ ] 消息：删除/清空与 stats
- [ ] 重启按钮（或 `allow-restart=false` 时的禁用提示）

---

## 13. 运维手册

### 13.1 常用命令

```bash
systemctl status ntfy-dashboard
systemctl restart ntfy-dashboard
journalctl -u ntfy-dashboard -f
ntfy-dashboard -version
```

### 13.2 升级

```bash
sudo install -m 0755 bin/ntfy-dashboard /usr/local/bin/ntfy-dashboard
sudo systemctl restart ntfy-dashboard
```

配置向后兼容：新增项均有默认值。升级后如涉及 `server.yml` 行为变化，进入配置页保存一次可完成规范化（例如 `auth-users` 列表化）。

### 13.3 回滚

- 二进制：保留上一版本文件，覆盖后重启；
- 配置：使用同目录 `server.yml.bak.<时间戳>` 恢复，再重启 ntfy；
- 仪表盘配置：恢复 `/etc/ntfy/dashboard.yml` 备份。

### 13.4 备份建议

- `/etc/ntfy/dashboard.yml`
- `/etc/ntfy/server.yml` 及其备份
- `auth-file` / `user.db`、`cache.db`
- systemd 单元
- 当前二进制与版本号

---

## 14. 已知限制与改进建议

### 14.1 已知限制

1. 消息存储仅支持 SQLite；PostgreSQL（`database-url`）不支持。
2. 删除消息直接操作 `cache.db`，与 ntfy 并发写入依赖 `busy_timeout`，极端情况下可能返回 database locked。
3. 单管理员、无 RBAC、无操作审计表（仅日志）。
4. 无登录限速/MFA；依赖反向代理加固。
5. 无消息发布/附件管理。
6. `server.yml` 仅有约 45 个白名单键；ntfy 新增配置需手动加入 `knownKeys`。
7. 删除频道消息不可恢复（备份 `cache.db` 可恢复）。
8. 原始配置预览会展示敏感字段（仅管理员可见）。

### 14.2 建议的后续迭代（按优先级）

1. **发布消息**：调用 ntfy HTTP API 发布测试消息（注意 auth token 管理）。
2. **操作审计**：落库记录谁在何时改了什么（当前仅进程日志）。
3. **登录保护**：失败次数限制、可选 TOTP。
4. **PostgreSQL 支持**：读取 `database-url` 的消息（可先只读）。
5. **配置项自动发现**：解析 ntfy 官方 `server.yml` 模板生成白名单，减少版本升级维护。
6. **多管理员/RBAC**：区分只读与可写角色。
7. **消息导出**：导出频道消息为 JSON/CSV。
8. **容器化部署**：提供 Dockerfile 与 compose 示例（重启功能需替代方案）。
9. **国际化**：当前界面为中文。

---

## 15. 交接清单

### 15.1 代码与产物

- [x] 源码（见目录结构）
- [x] `bin/ntfy-dashboard` (Linux/amd64）
- [x] 配置模板与 systemd 单元
- [x] 单元测试 25 项
- [x] `README.md` 部署文档
- [x] 本说明书

### 15.2 环境与权限（接手人需确认）

- [ ] 目标 Debian 服务器 SSH 访问权限
- [ ] ntfy 服务名、版本（`ntfy --version`）
- [ ] `server.yml` 路径、`auth-file`、`cache-file` 实际位置
- [ ] 仪表盘监听地址、反向代理与证书配置
- [ ] systemd 单元安装位置与运行用户
- [ ] 备份目录与恢复流程
- [ ] 管理员账号交接（**通过安全渠道传递，不要写入本文件**）

### 15.3 代码管理建议

当前目录**不是 git 仓库**。建议交接后立即：

```bash
git init
git add .
git commit -m "chore: import ntfy-dashboard v1.3.4"
```

确认 `.gitignore` 排除 `bin/`、`dashboard.yml`、`*.db`、`*.bak.*`、`*.log`，避免提交密钥与数据。

### 15.4 新增配置项/页面时的改法

**新增受管配置键**：

1. 在 `internal/settings/settings.go` 的 `knownKeys` 增加条目（Kind/描述/是否可编辑/是否需重启）；
2. 若不可删除，加入 `protectedKeys`；
3. 页面表格自动渲染，无需改模板；
4. 补充 `settings_test.go`。

**新增页面/接口**：

1. 在 `internal/server/server.go` 的 `routes()` 注册（POST 用 `s.auth` 包裹）；
2. POST 处理器开头调用 `s.requireCSRF`；
3. 需要模板时在 `internal/web/templates/<page>.html` 定义 `{{define "content"}}`，并在 `web.NewTemplates` 的页面列表中登记；
4. 数据通过 `s.render`/`s.renderMsg` 传入。

---

## 16. 附录

### 16.1 版本历史

### 16.2 常用 ntfy 命令参考

```bash
ntfy user --config /etc/ntfy/server.yml list
ntfy user --config /etc/ntfy/server.yml add --role user alice   # 密码经 NTFY_PASSWORD 环境变量
ntfy user --config /etc/ntfy/server.yml change-role alice admin
ntfy user --config /etc/ntfy/server.yml remove alice
ntfy access --config /etc/ntfy/server.yml alice
ntfy access --config /etc/ntfy/server.yml alice alerts read-only
ntfy access --config /etc/ntfy/server.yml --reset alice
```

密码非交互传入：

```bash
NTFY_PASSWORD='...' ntfy user --config /etc/ntfy/server.yml add --role user alice
NTFY_PASSWORD_HASH='$2a$10$...' ntfy user --config /etc/ntfy/server.yml add --role user alice
```

### 16.3 关键文件索引

| 关注点 | 文件 |
| --- | --- |
| 入口与优雅关闭 | `main.go` |
| 路由/中间件/渲染 | `internal/server/server.go` |
| 会话与 CSRF | `internal/server/auth.go` |
| 配置编辑规则 | `internal/settings/settings.go` |
| CLI 参数与输出解析 | `internal/ntfycli/ntfycli.go` |
| 数据库兼容与清理 | `internal/ntfydb/ntfydb.go` |
| 服务重启 | `internal/systemd/systemd.go` |
| 页面模板 | `internal/web/templates/*.html` |
| 样式与脚本 | `internal/web/static/*` |
| 配置模板 | `configs/dashboard.yml.example` |
| 部署单元 | `configs/ntfy-dashboard.service` |

---

**文档维护约定**：任何涉及配置保存语义、ntfy CLI 调用方式、数据库读写、重启行为的改动，都必须同步更新第 4 节「关键设计决策与踩坑记录」、第 15.4 节改法说明与版本历史。
