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
mkdir -p "$PREFIX/bin"
cp ./wg-cli-android-arm64 "$PREFIX/bin/wg-cli"
chmod 755 "$PREFIX/bin/wg-cli"
wg-cli --config
wg-cli --check
wg-cli curl https://api.ipify.org
```

不要从 Android 共享存储直接执行二进制；把文件复制到 Termux 私有目录。首次使用需要真实的服务端配置，仓库的 `examples/wg.conf` 只是占位模板。零依赖指运行时无需额外组件，源码构建需要 Go 和首次下载的模块依赖。

配置路径优先级：`-c` → `WG_CLI_CONFIG` → 可执行文件同目录下的 `wg.conf`（解析符号链接后的位置）。默认路径与当前工作目录无关；安装在 `$PREFIX/bin/wg-cli` 时，默认配置是 `$PREFIX/bin/wg.conf`。`-c` 的相对路径按当前工作目录解析。

`wg-cli --config` 启动配置向导，可导入现有配置，或逐项填写单个 Peer 的参数。交互终端中私钥与预共享密钥输入不回显。保存前验证配置，覆盖已有文件需明确输入 `yes`（默认取消），以 `0600` 权限原子保存；不会测试连接。多 Peer 配置可直接编辑文件或重新导入。

```sh
wg-cli --config                      # 配置可执行文件同目录下的 wg.conf
wg-cli -c ~/work/wg.conf --config     # 配置指定路径
wg-cli -c ~/work/wg.conf rclone lsd remote:
```

工具选项放在命令之前，后续参数原样传给子命令：

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
* 代理按目标 IP 分流：命中任一 Peer 的 `AllowedIPs` 时走 WireGuard，其他地址走系统网络。命中的 IP 连接失败时，不会把同一个 IP 改走直连。HTTP、CONNECT 及 `--dial` 均遵循此规则。
* DNS 查询使用配置里的 DNS IP，并按相同规则分流：DNS 在 `AllowedIPs` 内走隧道，在范围外走系统网络。解析后，再按每个目标 IP 选择线路。一个 DNS 失败后可尝试下一个已配置的 DNS，各自按路由规则连接，不会回退到未配置的系统 DNS。范围外的 DNS 查询会暴露给直连网络；客户端自行解析的域名不受此规则控制。WireGuard Endpoint 域名仍通过系统解析，若不需要这次解析可填写 IP。

例如，`AllowedIPs = 10.65.0.0/16`、`DNS = 223.5.5.5` 时，DNS 和公网目标走正常网络，`10.65.3.3` 走 WireGuard。希望所有对应地址均走隧道时，使用 `0.0.0.0/0`（IPv6 为 `::/0`）。此规则只适用于代理支持的连接，`ping` 仍不会被接管。

rclone 的代理行为见[官方 FAQ](https://rclone.org/faq/#can-i-use-rclone-with-an-http-proxy)和[选项文档](https://rclone.org/docs/#http-proxy)；Git 的覆盖规则见[官方 http.proxy 文档](https://git-scm.com/docs/git-config#Documentation/git-config.txt-httpproxy)。

## Git SSH

`git@github.com:...` 使用 SSH，不读取 HTTP_PROXY。`--dial` 提供 SSH ProxyCommand 所需的双向字节流，仍不需要 nc/proxychains。以下把当前的 wg-cli 路径及固定配置写进本次命令参数，没有修改全局 SSH 配置：

```sh
git -c core.sshCommand="ssh -o 'ProxyCommand=$PREFIX/bin/wg-cli -c $PREFIX/bin/wg.conf --dial %h:%p'" \
  clone git@github.com:user/repo.git
```

这里每个 SSH 连接各建一个临时隧道，不需再套外层 `wg-cli`。标准输出只传输数据，诊断写到标准错误。路径含空格时请改用 SSH 配置中的妥善引用写法。

## WireGuard 配置

支持标准 `[Interface]` 和多个 `[Peer]`，包括 PrivateKey、Address、DNS、MTU、PublicKey、PresharedKey、Endpoint、AllowedIPs、PersistentKeepalive。DNS 至少包含一个 IP，可位于 AllowedIPs 之外并通过系统网络访问；范围内的 DNS 则需要兼容的隧道 Address 和服务端连通性。DNS 字段不支持搜索域；缺少 DNS 时会报错，不会回退到系统 DNS。

保留 Address 的主机地址而非网段地址。ListenPort 接受但使用随机本地 UDP 端口，便于并发调用。MTU 范围为 1280–65535，默认 1420。PostUp/PostDown、路由表等 wg-quick 专属字段会报错，不会执行配置中的 shell 命令。需要从导出配置中移除不支持的字段。

服务端须已配置你的公钥、客户端隧道地址及相应转发/NAT。AllowedIPs 选择走隧道的目标，其他目标走系统网络；若希望所有 IPv4 目标走隧道，使用 `0.0.0.0/0`（IPv6 需同时配置 IPv6 地址及 `::/0`）。

`--check` 仅检查配置语法，不测试路由及握手；握手按首个网络请求触发，上游连接超时为 30 秒。工具不打印私钥；临时代理仅监听 127.0.0.1，并要求随机认证。认证令牌在子进程环境中，同一 Termux 用户可读，因此不能用来隔离同 UID 的恶意程序。HTTPS 保留客户端到目标的端到端 TLS，无证书替换。

命令退出码原样返回。找不到命令返回 127，无法执行返回 126，工具错误返回 1，用法错误返回 2。收到 INT/TERM/HUP 时转发给直接子进程，3 秒后必要时强制结束该进程并清理隧道。请使用前台命令；主动脱离、后台运行的后代不属于生命周期管理范围，外层命令结束后也无法继续使用临时代理。

## 连接故障排查

代理返回 `502` 表示无法连接上游服务，不代表 WireGuard 已成功握手。连接错误会同时写入 HTTP 响应和标准错误，包含所选线路和底层原因；隧道错误还会报告匹配 Peer 是否完成过握手，不输出密钥。

* **未成功握手**：检查 Endpoint、双方密钥、预共享密钥、服务端 Peer 设置及 UDP 连通性。
* **握手成功但连接被拒绝**：核对目标 IP、端口及服务监听状态；防火墙也可能主动拒绝连接。
* **握手成功但超时**：检查目标在线状态、服务端转发、防火墙和回程路由，仅凭超时无法确定具体原因。

示例里的 `8080` 只是占位端口，需要替换为目标实际提供 HTTP 服务的端口。`curl -v --fail-with-body` 可显示代理状态码及错误响应；普通 curl 收到 HTTP 502 时仍可能以成功状态退出。

```sh
wg-cli curl -v --fail-with-body http://10.65.3.3:8080/
```

## 构建与验证

```sh
git clone https://github.com/RainGather/wg-cli.git
cd wg-cli
go test ./...
go vet ./...
sh scripts/build.sh
```

建议 Go 1.25 或更新版本。构建脚本输出 Android arm64 单文件及 SHA256SUMS。运行无需 Go、NDK 或共享库。amd64 和 32 位 Android 不在当前发布目标内。

集成测试在本机创建两个真实 WireGuard 设备，通过 UDP 交换加密数据，在隧道内部署 DNS、HTTP、HTTPS 服务，验证 HTTP/CONNECT、代理认证、按 AllowedIPs 分流及命中地址失败不回退直连；DNS 测试覆盖直连和隧道两种线路。若本机安装 git/rclone，还会分别执行真实的 `git ls-remote` / `rclone lsf`；缺少时明确跳过。无需公网 VPN 凭据。Android 交叉编译通过不等同于真机验证，仍需用你的实际配置在 Termux 上验收。

采用官方 [wireguard-go 用户态网络栈](https://git.zx2c4.com/wireguard-go/tree/tun/netstack)，模块版本固定在 go.mod/go.sum。项目代码采用 [MIT 许可证](LICENSE)，允许使用、修改和再分发；第三方组件保留各自许可证，详见 [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES)。构建时这些文件随二进制一起复制到 dist，分发二进制时请保留许可证文件。
