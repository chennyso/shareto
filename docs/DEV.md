这份文档专为代码生成 Agent（如 Claude Code、Cursor、Devin 等）编写，采用**契约驱动（Contract-Driven）**与**零歧义工程规范**设计。你可以直接将以下全部内容复制作为 Prompt 喂给执行 Agent。

---

# `shareto` v3.0 生产级全量工程实现说明书 (Agent Execution Spec)

## 0. Agent 执行指令与全局约束 (System Instructions for Coding Agent)

你现在的任务是完整实现 Linux 极简安全大文件分发工具 `shareto` (v3.0)。请严格遵守以下工程红线：

1. **编译环境**：Go 1.22+，强制 `CGO_ENABLED=0`，目标平台 `GOOS=linux`。
2. **最小外部依赖**：仅允许引入 `golang.org/x/crypto`（用于 Argon2id）、`golang.org/x/sys/unix`（用于 `SO_PEERCRED` 与底层系统调用）、`golang.org/x/term`（用于终端无回显读密码）和 `[github.com/pelletier/go-toml/v2](https://github.com/pelletier/go-toml/v2)`（或使用标准库 JSON 替代配置以实现零第三方库）。**严禁引入 Gin/Echo 等重型 Web 框架**，全面使用 Go 1.22+ 增强版标准库 `net/http` 路由。
3. **无省略实现**：文件元数据验证、`pread` 流式读取、慢速连接断开、有界 LRU 限频和具有有效期的 HMAC CSRF 令牌均须完整实现。

---

## 1. 项目目录拓扑结构 (Directory Layout)

请严格按照以下目录结构创建文件：

```text
shareto/
├── go.mod
├── Makefile
├── cmd/
│   └── shareto/
│       └── main.go                 # 统一入口：解析 CLI 子命令 (share/list/revoke/daemon/status)
├── internal/
│   ├── config/
│   │   └── config.go               # 加载/生成默认配置 ($XDG_CONFIG_HOME/shareto/config.json)
│   ├── crypto/
│   │   └── auth.go                 # 128-bit Token生成、Argon2id哈希与恒定时间校验、CSRF Nonce
│   ├── netutil/
│   │   ├── ip.go                   # Trusted Proxies 解析、IPv4(/32)与IPv6(/64)与(/24)子网归一化
│   │   ├── ratelimit.go            # 固定容量(65536)并发安全 LRU 限频器
│   │   └── slowconn.go             # 滑动窗口最低速率检测 Conn/ResponseWriter 包装器
│   ├── store/
│   │   ├── model.go                # ShareEntry、DownloadSession 核心数据结构与 JSON 序列化
│   │   ├── filepin.go              # O_NOFOLLOW 打开文件、Inode/Mtime 快照核验、NewSectionReader
│   │   └── manager.go              # 内存状态管理、并发信号量控制、原子持久化 ($XDG_STATE_HOME)
│   ├── ipc/
│   │   ├── server.go               # Unix Domain Socket 服务端 (0600 + SO_PEERCRED 校验)
│   │   └── client.go               # CLI 调用的 IPC 客户端
│   └── httpd/
│       ├── server.go               # HTTP/HTTPS Server 生命周期、路由注册、安全响应头中间件
│       ├── handler_share.go        # GET /s/{token} 落地页、POST /s/{token}/claim、/d/... 流传输
│       └── templates.go            # 内联纯静态 HTML/CSS 模板 (零外部 CDN 依赖)
└── packaging/
    ├── shareto.service             # Systemd User Service 单元文件 (含内核沙箱指令)
    ├── postinst                    # Debian 安装后脚本 (含容器检测与 linger 提示)
    └── build-deb.sh                # 自动化构建 .deb 包脚本

```

---

## 2. 核心数据结构与状态契约 (`internal/store/model.go`)

### 2.1 配置文件结构 (`$XDG_CONFIG_HOME/shareto/config.json`)

为减少依赖，配置文件统一采用标准 JSON（若不存在则首次启动自动生成默认配置）：

```go
type Config struct {
    ListenAddr          string   `json:"listen_addr"`           // 默认 "127.0.0.1:38080"
    PublicBaseURL       string   `json:"public_base_url"`       // 默认 "https://share.example.com"
    TLS                 TLSConfig`json:"tls"`
    TrustedProxies      []string `json:"trusted_proxies"`       // 默认 ["127.0.0.1/32", "::1/128"]
    MaxGlobalStreams    int      `json:"max_global_streams"`    // 默认 64
    MaxConnections      int      `json:"max_connections"`       // 默认 256
    MaxConnectionsPerIP int      `json:"max_connections_per_ip"` // 默认 32
    MaxConcurrentAuth   int      `json:"max_concurrent_auth"`   // 默认 2
    MaxStreamsPerToken  int      `json:"max_streams_per_token"` // 默认 16
    MaxStreamsPerSession int     `json:"max_streams_per_session"`// 默认 8
    MaxActiveShares     int      `json:"max_active_shares"`     // 默认 100
    MinTransferRateKBps int      `json:"min_transfer_rate_kbps"`// 默认 16 (即 16KB/s)
}

type TLSConfig struct {
    Enabled  bool   `json:"enabled"`
    CertFile string `json:"cert_file"`
    KeyFile  string `json:"key_file"`
}

```

### 2.2 持久化状态结构 (`$XDG_STATE_HOME/shareto/state.json`)

文件权限强制设为 `0600`：

```go
type PersistentState struct {
    Version int                    `json:"version"` // 固定为 3
    Shares  map[string]*ShareEntry `json:"shares"`  // key: Token
}

type ShareEntry struct {
    Token              string                      `json:"token"`               // 22字符 base64url (128-bit)
    FilePath           string                      `json:"file_path"`           // EvalSymlinks 后的绝对物理路径
    FileName           string                      `json:"file_name"`           // 展示与下载文件名
    DeviceID           uint64                      `json:"device_id"`           // stat.Dev
    Inode              uint64                      `json:"inode"`               // stat.Ino
    FileSize           int64                       `json:"file_size"`           // 初始字节大小
    MtimeUnixNano      int64                       `json:"mtime_unix_nano"`     // 初始修改时间(纳秒)
    CtimeUnixNano      int64                       `json:"ctime_unix_nano"`     // 初始元数据变化时间(纳秒)
    PasswordHash       string                      `json:"password_hash,omitempty"` // Argon2id 编码字符串，空表示无密码
    CreatedAt          time.Time                   `json:"created_at"`
    ExpiresAt          time.Time                   `json:"expires_at"`
    MaxDownloads       int                         `json:"max_downloads"`       // 1 ~ 1000
    RemainingDownloads int                         `json:"remaining_downloads"`
    Sessions           map[string]*DownloadSession `json:"sessions"`            // key: SessionID

    // 运行时非导出字段 (json:"-")
    mu         sync.Mutex
    pinnedFile *os.File      // 常驻只读文件句柄
    authSem    chan struct{} // 容量为 2 的密码验证信号量，防 Argon2id CPU DoS
    activeConns atomic.Int32 // 当前活跃下载流计数
}

type DownloadSession struct {
    SessionID   string       `json:"session_id"`   // 22字符 base64url (128-bit)
    SubnetKey   string       `json:"subnet_key"`   // 绑定的客户端网段: IPv4为 /24, IPv6为 /64
    CreatedAt   time.Time    `json:"created_at"`
    ExpiresAt   time.Time    `json:"expires_at"`   // min(Now+2h, ShareEntry.ExpiresAt)
    activeConns atomic.Int32 `json:"-"`            // 当前 Session 并发流计数 (上限 8)
}

```

---

## 3. 核心安全模块实现规范 (Module Specifications)

### 3.1 文件句柄锁定与无状态并发读 (`internal/store/filepin.go`)

1. **打开与校验函数 `OpenAndPinFile(rawPath string) (*os.File, os.FileInfo, uint64, uint64, error)**`：
* 调用 `filepath.Abs(rawPath)` 和 `filepath.EvalSymlinks(absPath)` 获取真实路径 `realPath`。
* 使用系统调用级标志打开：`fd, err := unix.Open(realPath, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)`，再通过 `os.NewFile(uintptr(fd), realPath)` 转为 `*os.File`。
* 调用 `f.Stat()` 获取 `fi`：
* 若 `!fi.Mode().IsRegular()`，关闭文件并返回错误 `"only regular files can be shared"`。
* 若 `fi.Size() <= 0`，关闭文件并返回错误 `"empty files cannot be shared"`。


* 提取底层 `stat_t := fi.Sys().(*syscall.Stat_t)`，返回 `stat_t.Dev` 和 `stat_t.Ino`。


2. **完整性复核与并发安全读取器 `GetSectionReader(entry *ShareEntry) (*io.SectionReader, time.Time, error)**`：
* 对 `entry.pinnedFile` 调用 `Stat()`：
* 核对设备编号、inode、文件大小、mtime 和 ctime。平台相关的 ctime 提取位于 `filetime_linux.go` 与 `filetime_darwin.go`。
* `io.SectionReader` 的底层为 `verifiedReaderAt`，每次读取数据前后均检查元数据。检测到修改时返回零字节及 `ErrFileMutated`，本次读取结果不得发送给客户端。
* 尚未发送正文的请求返回 `410`；已经发送响应头的传输使用 `http.ErrAbortHandler` 终止连接，并通过管理器持久化撤销该分享。Range 请求继续通过独立的 `SectionReader` 使用 `pread` 读取文件。



### 3.2 密码学与防时序验证 (`internal/crypto/auth.go`)

1. **高熵 Token 生成 `GenerateToken() (string, error)**`：
* 读取 `crypto/rand` 16 字节，使用 `base64.RawURLEncoding.EncodeToString(b)` 返回 22 字符字符串。


2. **Argon2id 密码哈希与校验**：
* 参数固定：`time = 1`, `memory = 64 * 1024` (64MB), `threads = 2`, `keyLen = 32`, `saltLen = 16`。
* 存储格式：`$argon2id$v=19$m=65536,t=1,p=2$<base64_salt>$<base64_hash>`。
* 验证时通过 `Manager.AcquireAuthSlots` 同时获取全局及单个分享的验证名额，无法获取时立即返回 `503`。全部分享的默认并发上限为二，每个分享的并发上限为二。
* 通过 `RateLimiter.BeginAttempt` 原子预留一次验证尝试，无法预留时返回 `429`。验证完成后记录成功或失败，并释放两个层级的验证名额。
* 编码哈希的 Argon2id 参数、salt 长度和输出长度必须符合固定配置，再计算哈希并使用 `subtle.ConstantTimeCompare` 比较。



### 3.3 客户端 IP 提取与子网归一化 (`internal/netutil/ip.go`)

1. **`ExtractClientIP(r *http.Request, trustedCIDRs []*net.IPNet) net.IP`**：
* 解析 `r.RemoteAddr` 得到直连 IP `remoteIP`。
* 若 `remoteIP` 不在 `trustedCIDRs` 内，直接返回 `remoteIP`（忽略任何伪造的 `X-Forwarded-For`）。
* 若 `remoteIP` 命中可信代理，读取 `X-Forwarded-For`，按逗号分割并**从右向左**遍历，返回第一个**不属于** `trustedCIDRs` 的合法 IP；若全部属于可信代理，返回最左侧 IP。


2. **子网归一化函数**：
* `RateLimitKey(ip net.IP) string`：若为 IPv4（`ip.To4() != nil`），返回完整 IP 字符串（`/32`）；若为 IPv6，应用 `net.CIDRMask(64, 128)` 掩码后返回 `/64` 网段字符串。
* `SessionSubnetKey(ip net.IP) string`：若为 IPv4，应用 `net.CIDRMask(24, 32)` 返回 `/24` 网段；若为 IPv6，应用 `net.CIDRMask(64, 128)` 返回 `/64` 网段。



### 3.4 有界 LRU 限频器 (`internal/netutil/ratelimit.go`)

* 使用 `sync.Mutex` + 标准库 `container/list` + `map[string]*list.Element` 实现最大容量为 `65536` 的 LRU 缓存。
* 键格式：`fmt.Sprintf("%s|%s", token, RateLimitKey(clientIP))`。
* 值结构包含失败次数、时间窗口、锁定期限和正在验证的请求数量。
* 规则：
* 请求到达时，若 `Now < LockedUntil`，直接拒绝并返回剩余锁定秒数。
* 若密码错误或请求无效，在 1 分钟窗口内累加 `Failures`；当 `Failures >= 5` 时，设置 `LockedUntil = Now.Add(15 * time.Minute)`。
* 失败次数与正在验证的数量之和达到五次时拒绝新尝试。正在验证的条目保留在缓存中，容量不足且全部条目均有正在验证的请求时拒绝预留。
* 缓存容量最多 `65536`，插入新条目前删除最久未使用且没有正在验证请求的条目。



### 3.5 滑动窗口防慢速连接包装器 (`internal/netutil/slowconn.go`)

为防御 Slow-Read 攻击占满 64 个全局下载槽位，在下载流 Handler 中实现速率监控：

* 包装 `http.ResponseWriter` 为 `itoredWriter`：
* 记录每个统计周期（`window = 15 * time.Second`）内通过 `Write(p []byte)` 成功写入的字节数 `writtenBytes`。
* 启动一个伴随当前下载请求 Context 的后台 Ticker（每 15 秒触发一次）：
* 计算周期内写入字节是否小于 `minBytes = 16 * 1024 * 15`（即 `240 KB`，对应 `16 KB/s`）。
* 若小于阈值，通过 `http.NewResponseController(w)` 或底层 hijacker/conn 强制设置 `SetWriteDeadline(time.Now())` 中断底层连接，立即释放并发槽位。





---

## 4. HTTP 路由与两阶段会话交互契约 (`internal/httpd/`)

### 4.1 路由表

| 方法与路径 | 功能说明 | 是否扣减额度 |
| --- | --- | --- |
| `GET /s/{token}` | 渲染静态 HTML 落地页（或处理 `?raw=1` 直链重定向） | 否（`?raw=1` 除外） |
| `POST /s/{token}/claim` | 校验密码与 CSRF Nonce，原子扣减 1 次额度，创建 Session 并 `303` 重定向 | **是（原子扣 1）** |
| `GET /d/{token}/{session}/{filename}` | 校验 Session 与子网绑定，执行 `http.ServeContent` 支持 Range 并发流 | 否 |
| `HEAD /d/{token}/{session}/{filename}` | 同上，返回文件大小、`Accept-Ranges: bytes`、`ETag` 元信息 | 否 |

### 4.2 阶段一：`GET /s/{token}` 详解

1. 查找 `token`，若不存在、已过期或文件已被修改，返回 `404` 或 `410`。额度耗尽时拒绝新领取，既有下载会话保持其有效期。
2. **终端直链兼容模式**：
* 若该分享**无密码**，且满足：`r.URL.Query().Get("raw") == "1"` 或 `r.Header.Get("X-Shareto-Direct") == "1"`：
* 检查 `User-Agent` 黑名单（拦截包含 `MicroMessenger`, `TelegramBot`, `Twitterbot`, `Slackbot`, `Discordbot`, `facebookexternalhit`, `WhatsApp`, `Applebot` 的预览爬虫，若命中则仍返回普通 HTML 页）。
* 非爬虫的 `GET` 请求调用 `ClaimSession(token, clientIP)`，同步保存领取额度与 Session，成功后返回 `307`。`HEAD` 仅返回页面元数据，保存失败返回 `503`。


3. **正常浏览器访问**：
* 生成一个有效期 10 分钟的 HMAC-SHA256 签名 CSRF Token（载荷为 `token + "|" + clientSubnet + "|" + unixTimestamp`，密钥为守护进程启动时随机生成的 32 字节内存密钥）。
* 渲染内联 HTML 页面（展示文件名、格式化后的文件大小如 `4.28 GB`、剩余下载次数、过期倒计时、隐藏的 `<input name="csrf_token">`、可选的密码输入框及“立即下载”提交按钮）。



### 4.3 阶段二：`POST /s/{token}/claim` 详解

1. 使用 `http.MaxBytesReader(w, r.Body, 4096)` 限制请求体大小，HTTP Server 设置 5 秒请求读取期限，监听器限制全部连接和单个来源的连接数量。
2. 校验 `csrf_token` 签名、有效期、分享编号及客户端网段。
3. 检查该 `(token, clientIP)` 是否被限频器锁定；若锁定返回 `429 Too Many Requests`。
4. 若分享设置密码，申请全局和单个分享的验证名额，再原子预留尝试次数。Argon2id 验证失败返回 `403`，次数不足返回 `429`，验证名额不足返回 `503`。
5. **原子扣减与 Session 签发**：
* 加锁检查 `entry.RemainingDownloads > 0`；
* `entry.RemainingDownloads--`；
* 生成 22 字符 `sessionID`，记录 `SubnetKey: SessionSubnetKey(clientIP)`，有效期设为 `min(Now+2h, entry.ExpiresAt)`；
* 若当前 `len(entry.Sessions) >= 128`，淘汰最早创建的非活跃 Session；
* 同步保存状态，完成文件同步、原子重命名及目录同步；保存失败时恢复尚未提交的内存变更并返回 `503`；
* 返回 `303 See Other`，`Location: /d/{token}/{sessionID}/{url.PathEscape(entry.FileName)}`。



### 4.4 阶段三：`GET/HEAD /d/{token}/{session}/{filename}` 详解

1. 验证 `token` 与 `session` 是否存在且未过期。
2. 验证 `SessionSubnetKey(clientIP) == session.SubnetKey`，不匹配返回 `403 Forbidden`。
3. **三级并发槽位获取（非阻塞 CAS / Channel）**：
* 检查 `session.activeConns`（上限 8）、`entry.activeConns`（上限 16）、`globalActiveConns`（上限 64）。
* 任一超限立即返回 `429 Too Many Requests`（利用 `defer` 确保请求结束时精确递减三个计数器）。


4. 获取并发安全读取器 `reader, mtime, err := GetSectionReader(entry)`。
5. 注入严格响应头：
```go
w.Header().Set("Content-Type", "application/octet-stream")
w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="download.bin"; filename*=UTF-8''%s`, url.PathEscape(entry.FileName)))
w.Header().Set("X-Content-Type-Options", "nosniff")
w.Header().Set("Referrer-Policy", "no-referrer")
w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
w.Header().Set("Cache-Control", "private, no-transform, no-store")
w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, entry.Inode, entry.FileSize))

```


6. 使用 `SlowConnWrapper` 包装 `w`（仅针对 `GET` 请求启用速率监控），调用标准库：
`http.ServeContent(wrappedW, r, "", mtime, reader)`（传空文件名给 `ServeContent` 以阻止标准库覆盖 `Content-Type`）。

---

## 5. IPC 协议与 CLI 交互规范 (`internal/ipc/` & `cmd/shareto/`)

### 5.1 Unix Domain Socket 服务端 (`$XDG_RUNTIME_DIR/shareto/ipc.sock`)

1. 若 `$XDG_RUNTIME_DIR` 为空，回退至 `/run/user/<uid>`。确保父目录 `$XDG_RUNTIME_DIR/shareto` 权限为 `0700`。
2. 监听前先检查旧 Socket 是否存在，若无法连接则 `os.Remove` 清理残留文件；监听后立即 `os.Chmod(sockPath, 0600)`。
3. **连接级 `SO_PEERCRED` 校验**：
* 自定义 `net.Listener` 的 `Accept()` 包装：获取底层 `*net.UnixConn`，通过 `SyscallConn()` 调用 `unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)`。
* 若 `ucred.Uid != uint32(os.Getuid())`，立即关闭连接拒绝服务。


4. 在该 Unix Socket 上运行一个内部 `http.Server`（限制 `MaxBytesReader` 为 64KB），提供如下 JSON RPC 端点：
* `POST /ipc/share`：请求 `{ "file_path": "...", "expires_sec": 86400, "max_downloads": 10, "password_hash": "..." }` -> 响应 `{ "token": "...", "url": "https://...", "raw_url": "https://...?raw=1", "expires_at": "..." }`
* `GET /ipc/list`：返回当前所有活跃分享列表（含剩余次数、活跃连接数）。
* `POST /ipc/revoke`：请求 `{ "token": "..." }`（支持传入前 6 位前缀匹配或文件路径吊销）。
* `GET /ipc/status`：返回守护进程 PID、Uptime、全局活跃下载连接数。



### 5.2 CLI 命令设计 (`cmd/shareto/main.go`)

支持直观的 POSIX 风格参数与子命令：

```bash
# 1. 创建分享 (默认子命令：若第一个非 flag 参数不是已知子命令，则视为创建分享)
shareto [flags] <file_path>
  -t string   有效期 (支持 30m, 12h, 7d 等格式，默认 "24h"，范围 1m ~ 720h)
  -m int      最大下载会话次数 (默认 10，范围 1 ~ 1000)
  -p          启用密码保护 (交互式无回显读取，或从 SHARETO_PASSWORD 环境变量读取)
  --password-stdin  从标准输入读取密码

# 2. 管理子命令
shareto list                 # 列出当前所有有效分享 (表格展示 Token前缀、文件名、剩余次数、过期时间、活跃流)
shareto revoke <token|file>  # 立即吊销指定分享并关闭句柄
shareto status               # 查看守护进程状态与 Linger 状态
shareto daemon               # 启动后台守护进程 (供 systemd 调用)

```

### 5.3 CLI 自动环境自检 (Linger & Auto-Start)

当用户执行 `shareto <file>` 时：

1. 若连接 `ipc.sock` 失败，CLI 自动尝试执行 `exec.Command("systemctl", "--user", "start", "shareto.service").Run()` 并轮询等待最多 2 秒。
2. 检查 `/var/lib/systemd/linger/<username>` 文件是否存在：
* 若不存在，在终端用黄色高亮输出警告：
`[WARN] Systemd user lingering is NOT enabled. Shares will stop when you log out of SSH.`
`       Run once to fix: sudo loginctl enable-linger $USER`



---

## 6. 构建、打包与自验证脚本 (`Makefile` & `packaging/`)

### 6.1 `Makefile`

```makefile
VERSION ?= 3.0.0
ARCH ?= amd64
BINARY := dist/shareto

.PHONY: all build test clean deb

all: build

build:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build \
		-trimpath \
		-ldflags="-s -w -X main.Version=$(VERSION)" \
		-o $(BINARY) ./cmd/shareto

test:
	go test -v -race ./...

deb: build
	ARCH=$(ARCH) VERSION=$(VERSION) bash packaging/build-deb.sh

clean:
	rm -rf dist/

```

### 6.2 `packaging/build-deb.sh`

使用标准 `dpkg-deb --build --root-owner-group` 构建 `.deb` 包，目录映射如下：

* `dist/shareto` -> `/usr/bin/shareto` (`0755`)
* `packaging/shareto.service` -> `/usr/lib/systemd/user/shareto.service` (`0644`)
* `packaging/postinst` -> `DEBIAN/postinst` (`0755`)
* 生成 `DEBIAN/control`（`Architecture: amd64` 或 `arm64`，`Depends: systemd`）。

---

## 7. Agent 交付验收测试清单 (Verification Suite)

在完成代码编写后，请编写并运行 `internal/httpd/server_test.go` 自动化集成测试，必须 **100% 通过 `go test -race ./...**`：

1. **防爬虫误扣测试**：对无密码分享连续发起 20 次 `GET /s/{token}`，验证 `RemainingDownloads` 始终保持初始值 `1`，未被扣减。
2. **并发原子扣减测试**：设置 `MaxDownloads = 1`，启动 50 个并发 Goroutine 同时携带合法 CSRF Token 请求 `POST /s/{token}/claim`，验证**恰好只有 1 个协程**获得 `303` 重定向及 `session_id`，其余 49 个全部收到失败状态码。
3. **多线程 Range 并发安全测试**：使用获得的 `session_id` 启动 8 个并发 Goroutine，分别请求文件不同偏移量的 `Range: bytes=start-end`，拼接所有分片后计算 SHA-256，验证与原始文件 100% 一致，且 `-race` 无任何数据竞争告警。
4. **文件静默篡改熔断测试**：分享创建后，向原物理文件追加写入 1 字节，再次请求下载直链，验证服务端立即返回 `410 Gone`。
5. **软链接与特殊文件拦截测试**：尝试通过 IPC 注册 `/dev/zero` 或指向 `/etc/passwd` 的符号链接（当测试需要验证 `O_NOFOLLOW` 时），验证直接被拒绝或安全解引用。
6. **IPv6 `/64` 子网限频测试**：模拟来自同一 `/64` 前缀但不同后缀的 6 个 IPv6 地址（如 `2001:db8:1:2::1` 至 `::6`）连续提交错误密码，验证第 6 次请求即触发 `429 Too Many Requests` 子网级锁定。
