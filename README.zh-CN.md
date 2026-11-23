# wg-cli

[English](README.md) | 简体中文

在 Termux 中临时通过 WireGuard 执行命令。无 root，无需 WireGuard App、系统 TUN、`wg-quick` 或常驻 VPN；运行时只需一个可执行文件和你的 WireGuard 配置。待执行的 rclone、git 等程序需已安装。

```sh
wg-cli rclone lsd remote:
wg-cli rclone copy ./photos remote:backup/photos --progress
wg-cli git clone https://github.com/user/repo.git
wg-cli git fetch
wg-cli -c ~/my-wg.conf curl https://api.ipify.org
```

原理：进程内 WireGuard + TCP/IP 网络栈 → 临时本地 HTTP/CONNECT 代理 → 子命令。子命令结束后关闭代理和隧道。系统路由与父 shell 环境不变；多个调用各自使用随机端口，互不共享隧道。

## 安装到 Termux

先按下方步骤构建 `dist/wg-cli-android-arm64`，将它复制到手机 Termux 私有目录（多数手机是 arm64），然后执行：

```sh
mkdir -p "$PREFIX/bin" ~/.config/wg-cli
cp ./wg-cli-android-arm64 "$PREFIX/bin/wg-cli"
chmod 755 "$PREFIX/bin/wg-cli"
cp /path/to/your/wireguard.conf ~/.config/wg-cli/wg.conf
chmod 600 ~/.config/wg-cli/wg.conf
wg-cli --check
wg-cli curl https://api.ipify.org
```

不要从 Android 共享存储直接执行二进制；把文件复制到 Termux 私有目录。首次使用需要真实的服务端配置，仓库的 `examples/wg.conf` 只是占位模板。零依赖指运行时无需额外组件，源码构建需要 Go 和首次下载的模块依赖。

配置路径优先级：`-c` → `WG_CLI_CONFIG` → `$XDG_CONFIG_HOME/wg-cli/wg.conf` → `~/.config/wg-cli/wg.conf`。工具选项放在命令之前，后续参数原样传给子命令：

```sh
wg-cli -c ~/work.conf -- rclone sync ./data remote:data --progress
wg-cli sh -c 'git fetch && rclone lsd remote:'
```

## 支持范围

* rclone 的 HTTP/HTTPS 后端（如 WebDAV、S3 和各类云盘）自动使用代理环境变量。
* rclone SFTP/FTP 同时设置 `RCLONE_SFTP_HTTP_PROXY` / `RCLONE_FTP_HTTP_PROXY`，供支持这些选项的版本使用。后端专用配置或显式参数仍可能覆盖它们，需按所用版本验证；外部 SSH 程序需要另配转发。
* Git HTTP/HTTPS 使用 `http_proxy` / `https_proxy`；已有 `http.proxy`、URL 专用代理、`remote.<name>.proxy` 或命令行 `-c` 可能覆盖环境变量。需要时删除冲突配置，或显式传入临时代理：

```sh
wg-cli sh -c 'git -c http.proxy="$WG_CLI_PROXY" fetch'
```

* 设置大小写 HTTP/HTTPS/ALL_PROXY 并清空子进程的 NO_PROXY，避免继承旧代理或绕过规则。代理地址也以 `WG_CLI_PROXY` 提供给显式代理选项。
* 这是代理封装，不是强制网络隔离。忽略代理的程序、程序内绕过设置、本地地址的客户端自动绕过规则及独立的认证辅助程序仍可能直连。普通 UDP、ICMP/ping、HTTP/3 不受此 HTTP 代理接管；不提供 SOCKS 协议。
* 代理自身所有上游连接只能从 WireGuard 网络栈发出，失败不会回退直连。HTTP 目标域名及 CONNECT 中传入的域名在隧道内解析；客户端自行预解析的域名不在此保证内。WireGuard 的 Endpoint 域名在建立隧道前通过系统解析；希望避免这次解析可填写 Endpoint IP。

rclone 的代理行为见[官方 FAQ](https://rclone.org/faq/#can-i-use-rclone-with-an-http-proxy)和[选项文档](https://rclone.org/docs/#http-proxy)；Git 的覆盖规则见[官方 http.proxy 文档](https://git-scm.com/docs/git-config#Documentation/git-config.txt-httpproxy)。

## Git SSH

`git@github.com:...` 使用 SSH，不读取 HTTP_PROXY。`--dial` 提供 SSH ProxyCommand 所需的双向字节流，仍不需要 nc/proxychains。以下把当前的 wg-cli 路径及固定配置写进本次命令参数，没有修改全局 SSH 配置：

```sh
git -c core.sshCommand="ssh -o 'ProxyCommand=$PREFIX/bin/wg-cli -c $HOME/.config/wg-cli/wg.conf --dial %h:%p'" \
  clone git@github.com:user/repo.git
```

这里每个 SSH 连接各建一个临时隧道，不需再套外层 `wg-cli`。标准输出只传输数据，诊断写到标准错误。路径含空格时请改用 SSH 配置中的妥善引用写法。

## WireGuard 配置

支持标准 `[Interface]` 和多个 `[Peer]`，包括 PrivateKey、Address、DNS、MTU、PublicKey、PresharedKey、Endpoint、AllowedIPs、PersistentKeepalive。必须设置 DNS 为隧道内可达的 IP，并使 AllowedIPs 包含它。DNS 不支持搜索域；缺少 DNS 时不会使用系统 DNS 作为目标解析回退。

保留 Address 的主机地址而非网段地址。ListenPort 接受但使用随机本地 UDP 端口，便于并发调用。MTU 范围为 1280–65535，默认 1420。PostUp/PostDown、路由表等 wg-quick 专属字段会报错，不会执行配置中的 shell 命令。需要从导出配置中移除不支持的字段。

服务端须已配置你的公钥、客户端隧道地址及相应转发/NAT。AllowedIPs 限制了隧道可达目标；若希望所有代理目标均可访问，通常使用 `0.0.0.0/0`（IPv6 需同时配置 IPv6 地址及 `::/0`）。

`--check` 仅检查配置语法及 DNS 路由，不测试握手；握手按首个网络请求触发，上游连接超时为 30 秒。工具不打印私钥；临时代理仅监听 127.0.0.1，并要求随机认证。认证令牌在子进程环境中，同一 Termux 用户可读，因此不能用来隔离同 UID 的恶意程序。HTTPS 保留客户端到目标的端到端 TLS，无证书替换。

命令退出码原样返回。找不到命令返回 127，无法执行返回 126，工具错误返回 1，用法错误返回 2。收到 INT/TERM/HUP 时转发给直接子进程，3 秒后必要时强制结束该进程并清理隧道。请使用前台命令；主动脱离、后台运行的后代不属于生命周期管理范围，外层命令结束后也无法继续使用临时代理。

## 构建与验证

```sh
git clone https://github.com/RainGather/wg-cli.git
cd wg-cli
go test ./...
go vet ./...
sh scripts/build.sh
```

建议 Go 1.25 或更新版本。构建脚本输出 Android arm64 单文件及 SHA256SUMS。运行无需 Go、NDK 或共享库。amd64 和 32 位 Android 不在当前发布目标内。

集成测试在本机创建两个真实 WireGuard 设备，通过 UDP 交换加密数据，在隧道内部署 DNS、HTTP、HTTPS 服务，验证 HTTP/CONNECT、代理认证及无直连回退。若本机安装 git/rclone，还会分别执行真实的 `git ls-remote` / `rclone lsf`；缺少时明确跳过。无需公网 VPN 凭据。Android 交叉编译通过不等同于真机验证，仍需用你的实际配置在 Termux 上验收。

采用官方 [wireguard-go 用户态网络栈](https://git.zx2c4.com/wireguard-go/tree/tun/netstack)，模块版本固定在 go.mod/go.sum。项目代码采用 [MIT 许可证](LICENSE)，允许使用、修改和再分发；第三方组件保留各自许可证，详见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。构建时这些文件随二进制一起复制到 dist，分发二进制时请保留许可证文件。
