# shareto 服务架构与安全规范

---

## 1. 核心定位与威胁模型 (Threat Model)

### 1.1 核心愿景
`shareto` 是专为 Linux 终端用户设计的大文件外链直发工具：
- 在 SSH 终端一行命令声明分享；
- 朋友在手机/PC 浏览器打开链接直接以 TCP 满速下载；
- 采用 Go 纯静态二进制（`CGO_ENABLED=0`），打包为标准的 Debian/Ubuntu `.deb` 安装包，零外部运行时依赖。

### 1.2 威胁场景与边界界定
- **不信任网络传输层**：默认网络可能存在旁路嗅探、中间人（MITM）篡改；必须全程基于 TLS (HTTPS) 传输，杜绝明文 HTTP 传输密码和文件数据。
- **不信任外部请求者**：公网扫描器、暴力破解工具可能枚举 Token 或撞库 6 位数字密码；必须具备高强度熵值与动态限频退避机制。
- **本地权限最小化**：服务绝不使用 root 权限运行；必须防止任意文件泄露（如 `/etc/shadow` 或 `~/.ssh/id_ed25519`），即便通过 IPC 也严禁越权注册文件。
- **资源耗尽防御 (DoS)**：必须硬性限制并发连接数、单 IP 并发、超时阈值与条目上限，防止攻击者利用 Slowloris 或巨量并发拖垮系统。

---

## 2. 身份、权限模型与 IPC 安全

### 2.1 采纳单用户模型：Per-User Systemd 服务
为避免 root 守护进程带来的“整机读取”特权放大，`shareto` 采用 **Per-User（每用户守护进程）** 运行模型：

1. **服务身份**：
   - 运行在当前调用用户的上下文下（Systemd 用户服务：`systemctl --user start shareto`）；
   - 守护进程的 UID/GID 完全等同于用户本人，天然只能读取该用户本就有权访问的文件，从操作系统内核层彻底消除提权越权漏洞。
2. **Unix Domain Socket 权限隔离**：
   - 套接字路径设于标准运行时目录：`$XDG_RUNTIME_DIR/shareto/ipc.sock`（通常为 `/run/user/<UID>/shareto/ipc.sock`）；
   - 文件权限严格设定为 `0600`，且其父目录受内核保护，本机其他普通用户连探测或连接该 Socket 的权限都没有。
3. **安全的文件注册校验流程**：
   - **路径规范化**：通过 `filepath.Abs` 和 `filepath.EvalSymlinks` 彻底解开软链接，定位真实物理路径；
   - **普通文件校验**：严格验证 `fileInfo.Mode().IsRegular()`，直接拒绝设备文件（`/dev/...`）、目录、套接字或命名管道（FIFO）；
   - **文件句柄固定 (Handle Pinning)**：注册阶段以 `O_RDONLY | O_NOFOLLOW | O_CLOEXEC` 打开文件并保留句柄。每次 `ReadAt` 前后核对设备编号、inode、文件大小、mtime 和 ctime；检查失败时丢弃本次读取结果、撤销分享并终止传输。

---

## 3. 传输安全与网络拓扑 (TLS / 反向代理规范)

### 3.1 强制全链路 HTTPS (TLS 终结)
公网 HTTP 裸奔传输文件和密码属于重大隐患。`shareto` 在对外生成链接时**强制输出 `https://` 协议**，网络架构支持两种标准接入模式：

```
模式 A (受信任反向代理 / Tailscale Funnel / Caddy / Nginx):
  [公网客户端] ──── HTTPS ────> [前置代理 / Tailscale (终止TLS)]
                                       │ (127.0.0.1 纯内网转发)
                                       ▼
                              [shareto 守护进程 (监听 127.0.0.1)]

模式 B (直出独立 TLS 模式):
  [公网客户端] ──── HTTPS ────> [shareto 守护进程 (自载 TLS 证书)]
```

### 3.2 代理头与安全绑定规范
- **默认监听地址**：默认且强制仅绑定回环地址 `127.0.0.1:38080`（通过配置可开放双栈，但推荐前置反代）；
- **可信代理配置 (`trusted_proxies`)**：
  - 仅当请求来自受信任的反向代理 IP（如 `127.0.0.1`、`::1` 或指定代理 CIDR）时，才信任并解析 `X-Forwarded-For`、`X-Forwarded-Proto`；
  - 杜绝直接信任任意来源伪造的客户端 IP，保障限频策略准确。

---

## 4. 凭证安全、Token 熵与防暴力破解

### 4.1 128 位强随机 Token
- 放弃过短的 8 字符（32 位）示例；
- 使用 `crypto/rand` 读取系统安全随机源生成 **128 位（16 字节）高熵密钥**；
- 采用 URL-Safe 的 Base64（去掉填充，长度 22 字符）或 32 字符十六进制编码，空间达 $2^{128} \approx 3.4 \times 10^{38}$，完全不可猜测与遍历；
- 严禁在 stdout 或日志中输出未脱敏的完整访问凭据。

### 4.2 恒定时间密码比对 (Timing Attack Safe)
针对 `subtle.ConstantTimeCompare` 在长度不等时提前返回的缺陷，采用如下加固逻辑：
```go
func VerifyPassword(input, expected string) bool {
    // 先计算定长 SHA-256 哈希，彻底消除长度差异对比带来的时序泄露
    hInput := sha256.Sum256([]byte(input))
    hExpected := sha256.Sum256([]byte(expected))
    return subtle.ConstantTimeCompare(hInput[:], hExpected[:]) == 1
}
```

### 4.3 动态限频与指数退避 (Rate Limiting)
- **Token 级别 + 客户端 IP 级别双重限频**：
  - 每个 Token 针对单个 IP 限制每分钟最多尝试 5 次；
  - 连续输错 5 次后，强制锁定该 IP 针对该 Token 的访问 15 分钟；
  - 全部分享默认最多并发验证两个密码，每次使用 64 MiB Argon2id 工作内存；单个分享最多占用两个名额，无法取得名额的请求返回 `503`。
  - 正在验证的请求预留尝试次数，失败次数与正在验证的数量之和达到五次时拒绝继续验证。

### 4.4 敏感参数防历史记录泄露
- CLI 提供交互式隐藏输入（`shareto -p` 不跟参数时触发终端 `term.ReadPassword` 无回显输入）；
- 支持环境变量 `SHARETO_PASSWORD` 或 `--password-file`，防止明文密码留在 bash history 或 `ps aux` 中。

---

## 5. “下载次数”与 Range 并发会话模型

针对多线程下载器（IDM/迅雷）和浏览器 Range 请求会发出海量并发 `GET/HEAD` 的问题，建立**两阶段授权会话模型 (Two-Phase Session Model)**：

```
[客户端] ─── 1. 提交密码 / 请求直链 ───> [shareto Server]
                                              │ (验证通过)
                                              ▼
[客户端] <── 2. 下发短期 Session Token ───────┘ (此时原子扣减 1 次分享额度)
             (Cookie / URL Session)
     │
     ├── 并发 GET Range 0-100MB (携带 Session)   ──> 允许，计数不增加
     ├── 并发 GET Range 100-200MB (携带 Session) ──> 允许，计数不增加
     └── 恢复重试断点续传 (在 Session 2h 有效期内) ─> 允许，计数不增加
```

- **计数单位**：扣减“下载次数”以**成功建立下载授权会话 (Download Session)** 为准，而非每一个单独的 HTTP 请求或数据分片；
- **原子性保证**：通过 `sync.Mutex` 或 `atomic` 保证“检查额度 -> 扣减额度 -> 下发 Session”过程完全原子，杜绝并发竞争超额下载。

---

## 6. HTTP 安全响应头与文件流式传输

### 6.1 严格的浏览器安全响应头
为杜绝恶意文件直接在浏览器渲染导致 XSS 或利用浏览器 MIME 嗅探漏洞，所有下载及 HTML 页面必须强制注入以下响应头：
```http
Content-Disposition: attachment; filename="fallback.bin"; filename*=UTF-8''%E6%B8%B8%E6%88%8F.zip
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'
Cache-Control: private, no-transform
```
- 文件名执行 RFC 5987 / RFC 6266 标准的 UTF-8 转义，防止文件名注入污染 HTTP Header。
- 密码输入页面为纯静态内联 HTML/CSS，不加载任何第三方外部 CDN 资源。

### 6.2 流式传输承诺收敛
- 使用 Go 标准库 `http.ServeContent` 配合 `io.ReadSeeker` 实现标准的 Range 分片；
- **准确表述**：“采用定长流式分块传输，进程常驻内存平稳，不随被分享文件大小线性膨胀；由于启用 TLS 及代理头包装，底层是否触发 Linux `sendfile` 取决于传输层条件，不承诺全链路零拷贝”。

---

## 7. 资源上限与持久化设计

### 7.1 生产级 HTTP 超时与配额控制
```go
srv := &http.Server{
    ReadHeaderTimeout: 5 * time.Second,  // 防范 Slowloris 慢速请求头攻击
    ReadTimeout:       5 * time.Second,  // 限制完整请求的读取时间
    IdleTimeout:       60 * time.Second, // 及时回收空闲持久连接
    MaxHeaderBytes:    64 * 1024,        // 限制请求头大小 64KB
}
```
- **活跃连接与并发限额**：
  - 全部 HTTP 连接默认最多 256 个，单个来源 IPv4 地址或 IPv6 /64 网段默认最多 32 个；可信代理使用全部连接上限；
  - 单个 Token 最大并发连接数限制（默认 16）；
  - 全局最大并发下载连接数限制（默认 64）；
  - 最大存活分享条目限制（默认 100 条）。
- **参数范围严格校验**：
  - `-t` 有效期限制：最大不超过 30 天；
  - `-m` 下载限制：范围限定为 1 ~ 1000。

### 7.2 持久化与重启语义定义
- **默认策略：原子持久化**：
  - 分享规则持久化到 `$XDG_STATE_HOME/shareto/state.json`；
  - 状态变更按照同一个互斥锁串行提交，写入临时文件后执行文件同步、原子重命名和目录同步，完成后返回成功；
  - 领取会话保存失败时返回错误，并恢复尚未提交的领取额度和会话集合；撤销保存失败时返回错误，当前进程继续拒绝已经撤销的分享；
  - **重启行为**：加载尚未过期的规则并重新校验文件，保留额度已经耗尽且仍有有效下载会话的分享；状态文件损坏时启动失败；
  - **正在传输中断**：进程收到 `SIGTERM` 时执行 `server.Shutdown`，等待正在传输的请求最多 5 秒后退出。

---

## 8. 打包工程规范 (.deb & Makefile)

### 8.1 纯静态多架构编译
```makefile
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o dist/shareto cmd/shareto/main.go
```
- 严格禁止 cgo，确保产物为纯静态无外部 glibc 依赖的 ELF 二进制。

### 8.2 Debian 安装包元信息规范
- 包名：`shareto`
- 架构：明确标定 `amd64`（或 `arm64`，禁止模糊写作 all）
- 依赖：`systemd`
- `postinst` 安装后脚本具备容器检测逻辑（若处于 docker/chroot 或无 systemd 运行的无头环境中，仅安装二进制并警告，不触发报错退出）。
