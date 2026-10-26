# shareto

[![Go Version](https://img.shields.io/badge/Go-1.22%2B-00ADD8?logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Linux-FCC624?logo=linux&logoColor=black)](https://kernel.org)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

`shareto` 是专为 Linux 个人服务器与开发者打造的**极简、高安全、原子配额大文件临时分发工具**（v3.0）。

它通过内核级系统调用与契约驱动架构，提供比传统网盘、Alist 或网盘挂载工具更安全、更可控的大文件外链分发能力：支持阅后即焚（单次或多次下载额度）、子网绑定、并发限流、防爬虫误扣配额、防篡改熔断，且**全面拥抱 Go 1.22+ 原生标准库路由，零第三方重型 Web 框架**。

---

## 🌟 核心特性 (Key Features)

- 🔒 **文件句柄防篡改锁定**：
  - 采用系统调用级 `O_RDONLY | O_NOFOLLOW | O_CLOEXEC` 打开文件，禁止跨目录符号链接穿透。
  - 记录 `Dev`、`Inode`、`Mtime`、`Ctime` 与 `FileSize`，在每次读取数据前后检查元数据。检测到修改时丢弃本次读取结果并撤销分享；尚未开始传输的请求返回 `HTTP 410 Gone`，正在传输的响应立即终止。
  - 通过 `io.SectionReader`（基于系统调用 `pread`）实现 100% 协程安全的并发 HTTP Range 断点续传与多线程分片下载。
- 🛡️ **防爬虫误扣配额**：
  - 自动识别并拦截主流社交预览爬虫（微信、Telegram、Twitter/X、Slack、Discord、Facebook 等），防止聊天软件自动抓取卡片信息时偷跑一次性直链配额。
- ⚡ **两阶段会话与原子配额控制**：
  - **阶段一 (Landing/Direct)**：无消耗访问静态页面，或通过 `GET ?raw=1` 触发无密码直链跳转。`HEAD` 仅查询元数据。
  - **阶段二 (Claim)**：输入密码并校验 HMAC-SHA256 CSRF 令牌，原子扣减 1 次额度，签发 2 小时绑定的 `SessionID` 并 `303` 重定向。
  - **阶段三 (Download)**：客户端绑定网段校验（IPv4 `/24`，IPv6 `/64`），杜绝会话直链跨公网非法扩散。
- 🚦 **多层级并发限制与反滥用**：
  - 三级并发槽位 CAS 控制（全局上限 64、单个分享上限 16、单个会话上限 8）。
  - 固定容量 65536 条目的并发安全 LRU 限频器，同一分享下单 IP/子网 5 次密码错误锁定 15 分钟，正在验证的请求同时占用尝试次数。
  - 全部 HTTP 连接默认上限 256，单个来源 IP 默认上限 32；可信代理共享全部连接上限。请求头和请求体的读取期限均为 5 秒。
  - 防慢速读取（Slow-Read）监控：15 秒滑动窗口最低速率检测（低于 16KB/s 强制断开释放连接槽位）。
  - Argon2id 每次使用 64 MiB 内存，全部分享默认最多同时验证两个密码，工作内存预算为 128 MiB；验证名额已满时返回 `503`。
  - 创建分享、领取会话和撤销分享均等待状态文件同步、原子重命名及目录同步完成后返回成功。保存失败会向客户端返回错误，已在内存撤销的分享继续拒绝访问。
- 🔌 **内核级安全的进程间通信 (IPC)**：
  - CLI 与后台守护进程通过 Unix Domain Socket 通信（权限 `0600`）。
  - 连接时通过系统调用 `SO_PEERCRED` 校验发起端 UID，确保仅允许当前系统用户调用。
- 📦 **生产级 Systemd 用户服务**：
  - 内置内核沙箱安全指令（`ProtectSystem=strict`, `ProtectHome=read-only`, `NoNewPrivileges=true`）。
  - CLI 自动检测 Systemd 用户 linger 状态，给出操作建议。

---

## 🏗️ 交互工作流 (Workflow)

```mermaid
sequenceDiagram
    autonumber
    actor U as 访客客户端 / 浏览器
    participant S as shareto HTTP Server
    participant M as Store Manager (内存/状态)
    participant F as 磁盘文件 (pread)

    Note over U,S: 阶段一：访问落地页
    U->>S: GET /s/{token}
    S->>M: 校验 Token 是否有效
    S-->>U: 返回静态 HTML 落地页 (含 CSRF Nonce)

    Note over U,S: 阶段二：校验密码并获取会话
    U->>S: POST /s/{token}/claim (CSRF + 密码)
    S->>S: 校验 CSRF 签名与子网
    S->>S: Argon2id 恒定时间密码比对
    S->>M: 原子扣减 1 次 RemainingDownloads, 签发 SessionID
    S-->>U: 303 See Other (/d/{token}/{session}/{filename})

    Note over U,F: 阶段三：多线程并发流下载
    U->>S: GET /d/{token}/{session}/{filename} (带 Range)
    S->>S: 校验 Session 有效期及客户端子网归一化匹配
    S->>M: 申请全局/分享/会话 3 级并发槽位
    S->>F: 核对 Inode/Mtime/Size 快照并 pread 读取
    F-->>U: 206 Partial Content (受滑动窗口最低速率监控)
    Note over S: 请求结束，释放并发槽位
```

---

## 🚀 快速上手 (Quick Start)

### 1. 通过 APT 软件源安装 (Debian / Ubuntu 推荐)

我们提供了基于 GitHub Pages 维护的官方 APT 仓库，支持 `amd64` 与 `arm64`：

#### 一键安装
```bash
curl -fsSL https://huadaox.github.io/shareto/install.sh | sudo bash
```

#### 手动添加软件源
```bash
# 1. 导入 GPG 公钥
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://huadaox.github.io/shareto/key.gpg | sudo gpg --dearmor --yes -o /etc/apt/keyrings/shareto.gpg
sudo chmod a+r /etc/apt/keyrings/shareto.gpg

# 2. 添加 APT 软件源
echo "deb [signed-by=/etc/apt/keyrings/shareto.gpg] https://huadaox.github.io/shareto stable main" | sudo tee /etc/apt/sources.list.d/shareto.list > /dev/null

# 3. 安装 shareto
sudo apt update && sudo apt install -y shareto
```

### 2. 通过 GitHub Releases 下载安装包

前往 [GitHub Releases](https://github.com/huadaox/shareto/releases) 页面下载对应架构的包：

- **Debian / Ubuntu** (`.deb`):
  ```bash
  sudo dpkg -i shareto_<version>_amd64.deb
  ```
- **RHEL / CentOS / Rocky / Fedora** (`.rpm`):
  ```bash
  sudo rpm -ivh shareto-<version>-1.x86_64.rpm
  ```

### 3. 源码编译

环境要求：Linux，Go 1.22+。

```bash
git clone https://github.com/huadaox/shareto.git
cd shareto

# 编译单一二进制文件 (CGO_ENABLED=0)
make build
```
编译产物位于 `dist/shareto`。

### 4. 本地打包与构建 (.deb / .rpm / .tar.gz)

本项目内置 `nfpm` 打包工作流，一条命令自动编译并构建覆盖主流架构的安装包：

```bash
# 生成当前架构的 deb/rpm/tarball
make package

# 或一键编译生成全架构包 (amd64, arm64)
make package-all
```

本地安装说明：
- **Debian / Ubuntu** (`.deb`)：
  ```bash
  sudo dpkg -i dist/shareto_3.0.0_amd64.deb
  ```
- **RHEL / CentOS / Rocky / Fedora** (`.rpm`)：
  ```bash
  sudo rpm -Uvh dist/shareto-3.0.0-1.x86_64.rpm
  ```
- **通用便携包** (`.tar.gz`，适用于 Arch、Alpine 及任意 Linux 发行版)：
  ```bash
  tar -xzf dist/shareto_3.0.0_linux_amd64.tar.gz
  sudo cp shareto/shareto /usr/local/bin/
  ```

安装后启用 Systemd 用户后台守护进程：
```bash
# 启用并立即启动守护进程
systemctl --user enable --now shareto.service

# 开启持久化运行 (保证 SSH 断开后服务不中断)
sudo loginctl enable-linger $USER
```

---

## 💻 CLI 命令与使用示例

### 1. 运行交互式配置向导 (`shareto init`)

首次安装后，**强烈建议先运行 `shareto init`**。向导会自动探测本机公网 IPv6/局域网 IP 与端口可用性，并引导配置 HTTPS 证书：

```bash
shareto init
```
流程示例：
```text
1. Listen address and port (e.g. [::]:38080) [[::]:38080]: [::]:38080
   ✓ Port check passed for [::]:38080

2. Enable native HTTPS / TLS encryption? (y/N) [y]: y
   Certificate fullchain path (.crt/.pem): ~/.config/ssl/share.example.com.crt
   Private key path (.key/.pem): ~/.config/ssl/share.example.com.key
   ✓ Valid certificate loaded! CommonName: share.example.com, Expires: 2027-01-20

3. Public Base URL [https://share.example.com:38080]: https://share.example.com:38080

[✓] Configuration successfully saved to ~/.config/shareto/config.json
[✓] shareto.service restarted successfully!
```

### 2. 创建文件分享 (默认命令)

```bash
# 快速分享文件（默认有效期 24h，最大下载次数 10）
shareto /path/to/archive.iso

# 自定义有效期 (-t 支持 30m, 12h, 7d 等格式)
shareto -t 3d -m 5 /path/to/backup.tar.gz

# 启用密码保护（终端交互式无回显输入）
shareto -p /path/to/confidential.zip

# 管道输入密码或环境变量免交互
echo "my_secret_pass" | shareto --password-stdin /path/to/data.bin
# 或
SHARETO_PASSWORD="my_password" shareto -p /path/to/data.bin

# 临时覆盖基础 URL（适用于临时使用特定外网域名或 IP）
shareto --base-url https://share.example.com:38080 /path/to/file.iso
```

终端将输出公共访问链接：
```text
File successfully shared!
  Token:      m3l2Z4s8X91bC...
  Public URL: https://share.example.com/s/m3l2Z4s8X91bC...
  Direct URL: https://share.example.com/s/m3l2Z4s8X91bC...?raw=1
  Expires:    2026-10-23T23:30:00Z
  Max Claims: 5
```

> **终端直链下载模式**：若分享未设置密码，使用 `curl -L -OJ "<Public_URL>?raw=1"` 可直接重定向至流传输分片下载。

### 3. 查看当前分享列表

```bash
shareto list
```
输出示例：
```text
TOKEN       FILE             SIZE       REMAINING  STREAMS  AUTH      EXPIRES
m3l2Z4s8..  archive.iso      4.28 GB    3/10       2        None      2026-10-23T23:30:00Z
a90Kf21x..  backup.tar.gz    512.00 MB  1/5        0        Password  2026-10-24T12:00:00Z
```

### 4. 吊销指定分享

支持传入 Token 全称、前 6 位以上前缀、或原始物理文件路径：
```bash
shareto revoke m3l2Z4s8
# 或
shareto revoke /path/to/archive.iso
```

### 5. 查看守护进程状态

```bash
shareto status
```

---

## ⚙️ 配置文件与公网 HTTPS 说明

首次启动时，程序会在 `$XDG_CONFIG_HOME/shareto/config.json`（默认为 `~/.config/shareto/config.json`）自动生成默认配置：

```json
{
  "listen_addr": "127.0.0.1:38080",
  "public_base_url": "http://127.0.0.1:38080",
  "tls": {
    "enabled": false,
    "cert_file": "",
    "key_file": ""
  },
  "trusted_proxies": [
    "127.0.0.1/32",
    "::1/128"
  ],
  "max_global_streams": 64,
  "max_streams_per_token": 16,
  "max_streams_per_session": 8,
  "max_active_shares": 100,
  "min_transfer_rate_kbps": 16,
  "max_connections": 256,
  "max_connections_per_ip": 32,
  "max_concurrent_auth": 2
}
```

| 字段 | 默认值 | 作用说明 |
| :--- | :--- | :--- |
| `listen_addr` | `127.0.0.1:38080` | HTTP/HTTPS 监听地址（若直连外网需改为 `[::]:38080` 或 `0.0.0.0:38080`） |
| `public_base_url` | `http://127.0.0.1:38080` | 生成公共分享链接时使用的前缀（配置公网域名/HTTPS 时修改此处） |
| `tls.enabled` | `false` | 是否开启内置 TLS 加密（直连模式下推荐开启） |
| `tls.cert_file` | `""` | 证书路径（支持绝对路径或 `~/...` 用户目录路径） |
| `tls.key_file` | `""` | 私钥路径（支持绝对路径或 `~/...` 用户目录路径） |
| `trusted_proxies` | `["127.0.0.1/32", "::1/128"]` | 可信反向代理 CIDR 列表（仅受信任代理来源允许解析 `X-Forwarded-For`） |
| `max_global_streams` | `64` | 全局允许的最大活跃下载并发连接流 |
| `max_streams_per_token` | `16` | 单个分享链接允许的最大下载并发连接流 |
| `max_streams_per_session` | `8` | 单个会话允许的最大并发连接流（多线程分片下载） |
| `max_connections` | `256` | 全部 HTTP 连接数量上限，包括读取请求体和空闲连接 |
| `max_connections_per_ip` | `32` | 单个来源 IPv4 地址或 IPv6 /64 网段的连接上限；可信代理使用全部连接上限 |
| `max_concurrent_auth` | `2` | 全部分享的密码验证并发上限，范围 1 至 16，每个名额使用 64 MiB Argon2id 工作内存 |
| `min_transfer_rate_kbps`| `16` | 最低传输速率阈值（KB/s），低于该速率超 15s 强制掐断 |

连接配置必须满足 `1 <= max_connections_per_ip <= max_connections <= 65536`。已有配置缺少新增字段时使用默认值。状态文件新增 `ctime_unix_nano` 字段；读取已有版本 3 状态时，校验文件原有元数据后补充该字段。已耗尽领取额度且下载会话仍有效的分享会在重启后保留会话。

### 域名与 HTTPS 证书配置实战

#### 模式 A：原生内置 HTTPS 直连（推荐公网直连或动态 IPv6）

若你的机器拥有公网 IPv6 或公网 IP，直接使用内置 TLS 即可无需安装额外 Web 服务器：

1. **申请证书**：家庭宽带 80/443 被封时，推荐使用 `acme.sh` 通过 DNS-01 API 验证免费申请 Let's Encrypt 证书并安装到本地（例如 `~/.config/ssl/`）。
2. **修改配置**：
   ```json
   {
     "listen_addr": "[::]:38080",
     "public_base_url": "https://share.example.com:38080",
     "tls": {
       "enabled": true,
       "cert_file": "~/.config/ssl/share.example.com.crt",
       "key_file": "~/.config/ssl/share.example.com.key"
     }
   }
   ```
3. **重启守护进程**：`systemctl --user restart shareto.service`。

> 💡 **提示**：程序原生支持 `~` 自动展开为当前用户目录（如 `/home/user`）。

#### 模式 B：Nginx / Caddy 反向代理模式

若服务器已有 Nginx / Caddy 集中处理证书与 443 端口：
1. `shareto` 配置保持 `listen_addr: "127.0.0.1:38080"`，`tls.enabled: false`，`public_base_url: "https://share.example.com"`。
2. Nginx 配置核心指令：**必须关闭缓冲以支持大文件直通流传输**：
   ```nginx
   location / {
       proxy_pass http://127.0.0.1:38080;
       proxy_buffering off;
       proxy_read_timeout 3600s;
       proxy_set_header Host $host;
       proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
       proxy_set_header X-Forwarded-Proto https;
   }
   ```

---

## 🧪 自动化测试与工程验证

本项目包含严苛的端到端集成测试套件，全面开启 `-race` 数据竞争检测：

```bash
make test
```

测试覆盖清单：
1. **防爬虫误扣测试**：模拟多种社交平台爬虫 User-Agent 连续抓取 20 次，验证配额零误扣。
2. **并发原子扣减测试**：设置额度为 1，50 个并发 Goroutine 争抢 Claim，验证**恰好仅有 1 个请求**成功并签发 Session。
3. **多线程 Range 并发安全测试**：8 协程分片下载大文件并核验 SHA-256 哈希，验证并发 `pread` 零竞争。
4. **文件篡改熔断测试**：模拟向底层文件静默追加 1 字节，验证后续请求立即熔断返回 `410 Gone` 并清理分享。
5. **设备文件拦截**：拦截 `/dev/zero` 等特殊字符设备或空文件。
6. **IPv6 /64 子网限频测试**：模拟同 `/64` 网段不同后缀的连续爆破，验证第 6 次精准触发 `429 Too Many Requests`。

---

## 📄 License

MIT License. See [LICENSE](LICENSE) for details.
