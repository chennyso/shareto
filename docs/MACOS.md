# MacBook 安装与运行

## 安装 Universal 安装包

`shareto_<version>_darwin_universal.pkg` 同时包含 Apple Silicon 和 Intel 的原生程序。
发布安装包要求 macOS 13 Ventura 或以后版本。
使用 macOS 安装器打开安装包，程序安装至 `/usr/local/bin/shareto`，使用说明安装至 `/usr/local/share/doc/shareto`。

安装完成后，以当前登录用户的身份执行：

```bash
shareto version
shareto init
shareto service install
shareto /path/to/archive.zip
```

安装包安装命令行程序。`shareto service install` 注册当前用户的 launchd LaunchAgent，并启动后台进程。
后台服务在用户登录期间运行，用户退出登录后停止，在下次登录时启动。
创建分享时，如果后台服务尚未启动，CLI 会自动安装并启动当前用户的 LaunchAgent。

## 安装到用户目录

解压 `shareto_<version>_darwin_universal.tar.gz`，在解压目录中执行：

```bash
cd shareto
bash install.sh
export PATH="$HOME/.local/bin:$PATH"
shareto service install
```

可以给 `install.sh` 传入安装前缀，例如 `bash install.sh /path/to/prefix`。安装后的程序位于该目录的 `bin/shareto`。
将对应的 `bin` 目录加入终端配置中的 `PATH`。

## 管理后台服务

```bash
shareto service status
shareto service start
shareto service stop
shareto service restart
shareto service uninstall
```

`service uninstall` 停止服务并删除用户 LaunchAgent，保留配置和分享状态。
需要前台运行时执行 `shareto daemon`。

## 文件位置与权限

| 文件 | macOS 默认位置 |
| --- | --- |
| 配置 | `~/Library/Application Support/shareto/config.json` |
| 分享状态 | `~/Library/Application Support/shareto/state.json` |
| IPC socket | `~/Library/Caches/shareto/ipc.sock` |
| LaunchAgent | `~/Library/LaunchAgents/io.github.huadaox.shareto.plist` |
| 运行日志 | `~/Library/Logs/shareto/stdout.log`、`stderr.log` |

支持使用 `XDG_CONFIG_HOME`、`XDG_STATE_HOME` 和 `XDG_RUNTIME_DIR` 指定目录。
安装或重启 LaunchAgent 时会保存这些环境变量，目录必须使用绝对路径。
Unix socket 路径受 macOS 内核长度限制，应选择较短的目录路径。

CLI 和后台进程通过 `LOCAL_PEERCRED` 检查对端用户身份；socket 权限为 `0600`。
macOS 对“桌面”“文稿”“下载”等目录的隐私授权也适用于后台服务。
共享这些目录中的文件时，需要在系统设置中允许相应程序访问。

## 构建与验证

需要 Go 1.23.2 或以后版本，以及 Apple Command Line Tools。

```bash
make test
make build
make macos
```

`make build` 构建当前电脑的原生程序。
`make macos` 构建两种架构，生成 Universal 程序、`.pkg`、`.tar.gz` 和 `sha256sums-macos.txt`。
程序使用 ad-hoc 代码签名；安装包未使用 Developer ID Installer 证书签名，也未进行 Apple 公证。发布者身份签名与公证需要维护者配置对应的证书和凭据。
发布文件的 SHA-256 校验值随安装包一同提供。
