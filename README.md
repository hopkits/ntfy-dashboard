# ntfy-dashboard

一个独立的 Go 单二进制 Web 仪表盘，用于管理本机自行部署的 [ntfy](https://docs.ntfy.sh/) server。

ntfy 本身只提供命令行管理方式（`ntfy user`、`ntfy access` 等）。本项目把这些能力集中到一个带登录认证的 Web 控制台，并提供服务器配置编辑、频道与消息浏览/清理等运维功能。

> 完整的架构、设计决策、测试记录与交接信息见 **[docs/PROJECT_HANDBOOK.md](PROJECT_HANDBOOK.md)**。

---

## 功能

| 模块 | 能力 |
| --- | --- |
| 服务器配置 | 读取/编辑 `/etc/ntfy/server.yml`；**保存时只写入有值的配置项，留空项自动删除**；保存前 YAML 校验、自动备份、原子写入、保留注释与自定义键；保存与重启为两个独立按钮 |
| 用户管理 | 列出用户与权限；添加/删除用户、改密码、改角色；设置主题级权限（read-write / read-only / write-only / deny）；重置访问规则 |
| 频道与消息 | 浏览 `cache.db` 中的频道与消息；按标题/正文搜索、分页；删除单条消息、清空频道；展示缓存统计 |
| 安全 | 仪表盘自身登录认证（bcrypt + HMAC 签名会话 cookie）、CSRF 双提交校验、安全响应头、请求体大小限制 |

## 设计取舍

仪表盘不重新实现 ntfy 的内部逻辑，而是按能力选择最稳妥的路径：

| 操作 | 实现方式 | 原因 |
| --- | --- | --- |
| 用户/权限变更 | `exec` 调用官方 `ntfy` CLI（密码以 `NTFY_PASSWORD_HASH` 环境变量非交互传入） | 与 ntfy 自身的哈希、校验、审计逻辑保持一致 |
| 频道/消息读取 | 只读/受控打开 `cache.db`（SQLite） | ntfy 无查询缓存消息的 CLI/HTTP 接口 |
| 服务器配置 | 直接读写 `server.yml`（`yaml.Node` 原地修改） | 保留注释与未知配置项，避免破坏手工维护的文件 |

> 删除消息/清空频道会直接修改 `cache.db`，并同步维护 `stats` 表计数。运行时 ntfy 与本程序并发访问同一 SQLite 文件，已启用 `busy_timeout`；仍建议在低峰期执行清理操作。

## 兼容性

- 通过 `PRAGMA table_info` 自动探测 `messages` 表结构，兼容新旧版 ntfy 的 `cache.db`（旧版 `id` 主键 / 新版 `mid` 主键）。
- 若 ntfy 使用 `database-url`（PostgreSQL）作为消息存储，频道/消息页不可用（会明确提示），用户与配置管理不受影响。
- 已在 Go 1.27 上编译验证；建议 Go 1.22+。

## 目录结构

```
.
├── main.go                     # 入口：配置加载、登录密码处理、优雅关闭
├── internal/
│   ├── config/                 # 仪表盘自身配置（/etc/ntfy/dashboard.yml）
│   ├── ntfycli/                # ntfy CLI 封装（用户/权限）
│   ├── ntfydb/                 # cache.db 读取与清理
│   ├── settings/               # server.yml 读取/编辑（保留注释、备份、原子写入）
│   ├── server/                 # HTTP、认证、CSRF、页面处理器
│   └── web/                    # 内嵌模板与静态资源
├── configs/
│   ├── dashboard.yml.example   # 仪表盘配置示例
│   └── ntfy-dashboard.service  # systemd 单元
├── build.sh / Makefile
└── README.md
```

## 构建

需要 Go 工具链：

```bash
make test      # 运行单元测试
make build     # 输出 bin/ntfy-dashboard
# 或
GOOS=linux GOARCH=amd64 ./build.sh
```

交叉编译无需 CGO（SQLite 驱动为纯 Go 实现）。

## 安装部署

### 1. 安装二进制

```bash
sudo make install
# 等价于：install -m 0755 bin/ntfy-dashboard /usr/local/bin/ntfy-dashboard
```

### 2. 创建仪表盘配置

```bash
sudo install -m 0600 -D configs/dashboard.yml.example /etc/ntfy/dashboard.yml
```

生成管理员密码哈希（交互式，不回显）：

```bash
ntfy-dashboard -hash-password
```

把输出填入 `/etc/ntfy/dashboard.yml` 的 `auth.password-hash`。可选地生成会话密钥：

```bash
ntfy-dashboard -gen-secret
# 填入 auth.secret
```

最小可用配置：

```yaml
listen: ":8090"          # 监听所有网卡；仅本机访问请改为 127.0.0.1:8090
auth:
  username: "admin"
  password-hash: "$2a$10$...."
ntfy:
  server-config-file: "/etc/ntfy/server.yml"
  cli-path: "/usr/bin/ntfy"
```

> 若 `password-hash` 留空，程序会在启动时读取环境变量 `NTFY_DASH_PASSWORD` 并即时哈希。适合用 systemd `EnvironmentFile` 注入，避免把哈希写入文件。

### 3. 安装 systemd 单元

```bash
sudo install -m 0644 configs/ntfy-dashboard.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now ntfy-dashboard
sudo systemctl status ntfy-dashboard
journalctl -u ntfy-dashboard -f
```

默认监听 `:8090`，即服务器所有网卡（IPv4 + IPv6）。可通过 `-listen` 参数（如 `-listen 0.0.0.0:8090`、`-listen 127.0.0.1:8090`）或配置文件覆盖。

对外监听时的安全建议：

- 用防火墙限制可访问该端口的来源（例如只放行管理网段）；
- 强烈建议经 Nginx/Caddy 反代启用 HTTPS，并设置 `session.secure: true`；
- 若监听非回环地址且 `session.secure=false`，程序启动时会打印安全告警。

### 4. 反向代理 + HTTPS（推荐）

Nginx 示例：

```nginx
server {
    listen 443 ssl http2;
    server_name ntfy-admin.example.com;

    ssl_certificate     /etc/letsencrypt/live/ntfy-admin.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/ntfy-admin.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

启用 TLS 后建议把配置中的 `session.secure` 设为 `true`。

## 权限说明

systemd 单元默认以 `root` 运行，因为需要：

1. 写 `/etc/ntfy/server.yml`（通常 `root:root 0640`）；
2. 读写 `/var/lib/ntfy` 或 `/var/cache/ntfy` 下的数据库；
3. 以 ntfy 的 auth 数据库执行 CLI 管理命令。

单元已启用 `ProtectSystem=strict`、`NoNewPrivileges`、`PrivateTmp` 等加固，并通过 `ReadWritePaths` 限制可写目录。

如需以专用用户运行，可创建 `ntfydash` 用户，并把 `/etc/ntfy/server.yml` 与数据库文件授权给该用户（或组），同时调整单元的 `User=`/`Group=`。

## 使用

1. 浏览器打开 `http://<服务器IP>:8090`（或反代域名），使用配置的管理员账号登录。
2. **概览**：消息/频道/用户/缓存统计，最近消息，CLI 与缓存健康状态。
3. **服务器配置**：编辑白名单字段。保存时**只写入有值的配置项，留空的字段会从文件中删除**（避免空的 `database-url`、`listen-https` 等导致 ntfy 启动失败）；有值但不在白名单中的自定义键会原样保留。原文件备份为 `server.yml.bak.<时间戳>`。保存只改文件，不会重启服务；需要点击「ntfy 服务状态」卡片中的「重启 ntfy 服务」按钮才能生效。服务名与重启权限由 `ntfy.service-name`、`ntfy.allow-restart` 控制。

   `auth-users` 为**受保护项**：可以编辑，但留空不会删除；页面中用英文逗号分隔多个用户，保存时会写成 ntfy 要求的 **YAML 列表**（写成字符串会导致 ntfy 报 `Mismatched type for flag 'auth-users'`）。保存前还会校验 `user:bcrypt哈希:role` 格式，避免写坏导致无法登录或服务无法启动。
4. **用户管理**：添加用户（密码至少 8 位）、改角色/密码、删除用户、设置或重置主题权限。
5. **频道与消息**：查看频道列表与消息，支持搜索、分页、删除单条、清空频道。

## 配置项（`/etc/ntfy/dashboard.yml`）

| 键 | 说明 | 默认 |
| --- | --- | --- |
| `listen` | 仪表盘监听地址（`:8090` = 所有网卡） | `:8090` |
| `base-url` | 仪表盘对外 URL | 空 |
| `auth.username` | 管理员用户名 | `admin` |
| `auth.password-hash` | bcrypt 密码哈希 | 空（回退到 `NTFY_DASH_PASSWORD`） |
| `auth.secret` | 会话签名密钥 | 空（由密码哈希派生） |
| `session.ttl` | 会话有效期 | `12h` |
| `session.cookie-name` | 会话 cookie 名 | `ntfydash_session` |
| `session.secure` | 仅 HTTPS 发送 cookie | `false` |
| `ntfy.server-url` | ntfy 对外地址（仅用于展示链接） | 空 |
| `ntfy.cli-path` | `ntfy` 可执行文件路径 | `ntfy` |
| `ntfy.data-dir` | ntfy 数据目录 | `/var/lib/ntfy` |
| `ntfy.cache-db` | `cache.db` 路径（可选显式覆盖） | 空：取 `server.yml` 的 `cache-file`；未配置则提示未配置 |
| `ntfy.user-db` | `user.db` 路径 | `<data-dir>/user.db` |
| `ntfy.server-config-file` | `server.yml` 路径 | `/etc/ntfy/server.yml` |
| `ntfy.cli-timeout` | CLI 调用超时 | `30s` |
| `ntfy.service-name` | ntfy 的 systemd 服务名 | `ntfy` |
| `ntfy.systemctl-path` | systemctl 路径 | `systemctl` |
| `ntfy.allow-restart` | 是否允许从仪表盘重启 ntfy | `true` |
| `ntfy.restart-timeout` | 重启/状态查询超时 | `30s` |

## 命令行

```bash
ntfy-dashboard -config /etc/ntfy/dashboard.yml   # 启动服务
ntfy-dashboard -listen 0.0.0.0:8090              # 覆盖为所有 IPv4 网卡
ntfy-dashboard -listen 127.0.0.1:9000            # 仅本机
ntfy-dashboard -hash-password                    # 生成 bcrypt 密码哈希
ntfy-dashboard -gen-secret                       # 生成随机会话密钥
ntfy-dashboard -version                          # 输出版本
```

## 故障排查

| 现象 | 排查方向 |
| --- | --- |
| 概览提示「未配置消息缓存」 | `server.yml` 未设置 `cache-file`，ntfy 使用内存缓存，属正常状态；需要持久化消息时配置 `cache-file` 并重启 ntfy |
| 概览提示「无法读取消息缓存」 | 检查 `server.yml` 的 `cache-file` 路径、文件是否存在、运行用户是否有读权限 |
| 概览提示「无法调用 ntfy CLI」 | 检查 `ntfy.cli-path` 是否指向可执行文件（`command -v ntfy`） |
| 用户页报权限错误 | 确认仪表盘运行用户能读 `auth-file`/`user.db`，且 CLI 能以该用户身份写库 |
| 保存配置报「YAML 不合法」 | 待写入的值类型与键不匹配（如布尔键填了非 `true/false`），检查表单输入 |
| 配置已保存但未生效 | 保存不会自动重启；点击页面「重启 ntfy 服务」按钮，或执行 `systemctl restart ntfy` |
| 保存后 ntfy 启动失败 | 检查是否手动填入了无效值；用 `server.yml.bak.<时间戳>` 回滚。留空字段现会被删除，不再写入空值 |
| 页面重启报 `Unit ntfy.service not found` | 服务名不对（用 `systemctl list-units | grep ntfy` 确认），或 ntfy 运行在 Docker 中（此时应设 `allow-restart: false`） |
| 页面重启报 D-Bus / 沙箱相关错误 | systemd 单元的安全加固可能限制了对 systemd 的访问，可尝试在单元中增加 `ReadWritePaths=/run`，或改用 `allow-restart: false` 由外部脚本重启 |
| 修改配置导致 ntfy 无法启动 | 用 `server.yml.bak.<时间戳>` 恢复后重启 |
| 登录后立即失效 | 修改过 `auth.password-hash` 且未设置 `auth.secret`（会话密钥随密码变化） |

## 开发

```bash
go test ./...     # 单元测试
go vet ./...      # 静态检查
make build        # 构建
```

前端为 Go `html/template` + 原生 CSS/JS，通过 `go:embed` 内嵌，无前端构建步骤。
