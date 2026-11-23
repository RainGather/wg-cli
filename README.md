# wg-cli

English | [简体中文](README.zh-CN.md)

Run proxy-aware commands through a temporary, rootless WireGuard tunnel in Termux. No WireGuard app, system TUN device, `wg-quick`, or persistent VPN is required. At runtime, you need only the executable, your WireGuard configuration, and the command you want to run.

```sh
wg-cli rclone lsd remote:
wg-cli rclone copy ./photos remote:backup/photos --progress
wg-cli git clone https://github.com/user/repo.git
wg-cli git fetch
wg-cli -c ~/my-wg.conf curl https://api.ipify.org
```

wg-cli embeds WireGuard and a TCP/IP stack, starts a temporary authenticated HTTP/CONNECT proxy, and passes proxy environment variables to the child command. The proxy and tunnel close when the command exits. Each invocation uses its own tunnel and random local port.

## Install in Termux

Build `dist/wg-cli-android-arm64` using the instructions below, then copy it into Termux's private storage. Most current Android phones use arm64.

```sh
mkdir -p "$PREFIX/bin"
cp ./wg-cli-android-arm64 "$PREFIX/bin/wg-cli"
chmod 755 "$PREFIX/bin/wg-cli"
wg-cli --config
wg-cli --check
wg-cli curl https://api.ipify.org
```

Do not execute the binary directly from Android shared storage. Supply a working configuration from your WireGuard server or provider; [`examples/wg.conf`](examples/wg.conf) contains placeholders only.

Zero dependencies means no additional runtime components for wg-cli. Building from source requires Go and module downloads. Programs such as rclone, Git, curl, and SSH must already be installed when you use them.

## Usage

```text
wg-cli [-c wg.conf] [--] command [args...]
wg-cli [-c wg.conf] --dial host:port
wg-cli [-c wg.conf] --check
wg-cli [-c wg.conf] --config
wg-cli --version
```

Configuration path precedence: `-c`, then `WG_CLI_CONFIG`, then `wg.conf` beside the executable (after resolving symlinks). This is independent of your working directory; a binary installed as `$PREFIX/bin/wg-cli` defaults to `$PREFIX/bin/wg.conf`. Relative `-c` paths are resolved from the current working directory.

`wg-cli --config` starts a wizard to import an existing file or enter settings for one peer. Private keys and preshared keys are hidden when typed in an interactive terminal. The wizard validates before saving, asks before replacing an existing file (default: no), and atomically saves with permissions `0600`. It does not test connectivity. To update a multi-peer setup, edit the file or import its revised version.

```sh
wg-cli --config                      # Configure wg.conf beside the executable
wg-cli -c ~/work/wg.conf --config     # Configure a specific path
wg-cli -c ~/work/wg.conf rclone lsd remote:
```

Put wg-cli options before the command; subsequent arguments pass through unchanged. To run multiple commands in one tunnel, use a foreground shell:

```sh
wg-cli -c ~/work.conf -- rclone sync ./data remote:data --progress
wg-cli sh -c 'git fetch && rclone lsd remote:'
```

## Compatibility and routing

* **rclone HTTP/HTTPS backends**, including WebDAV, S3, and cloud storage providers, use standard proxy environment variables.
* **rclone SFTP/FTP:** wg-cli also sets `RCLONE_SFTP_HTTP_PROXY` and `RCLONE_FTP_HTTP_PROXY` for versions supporting these settings. Backend configuration or explicit flags can override them; verify your backend and version. External SSH processes need separate forwarding configuration.
* **Git HTTP/HTTPS** uses the proxy environment. Existing `http.proxy`, URL-specific settings, `remote.<name>.proxy`, or command-line options may override it. Remove conflicting settings or explicitly pass the temporary proxy:

```sh
wg-cli sh -c 'git -c http.proxy="$WG_CLI_PROXY" fetch'
```

wg-cli sets uppercase and lowercase HTTP/HTTPS/ALL_PROXY variables and clears NO_PROXY for the child. The proxy URL is also available as `WG_CLI_PROXY` for explicit application options.

This is a proxy wrapper, not enforced network isolation. Commands that ignore proxies, application bypass rules, clients that automatically bypass local addresses, and independent credential helpers may still connect directly. The proxy does not provide SOCKS or intercept arbitrary UDP, ICMP/ping, or HTTP/3.

All upstream connections made by the proxy use the WireGuard stack, with no direct fallback. HTTP target names and hostnames received in CONNECT requests resolve inside the tunnel. Names resolved independently by the client are outside this guarantee. The WireGuard Endpoint hostname resolves through the host before tunnel startup; use an Endpoint IP to avoid that lookup.

See the official [rclone proxy FAQ](https://rclone.org/faq/#can-i-use-rclone-with-an-http-proxy), [rclone proxy options](https://rclone.org/docs/#http-proxy), and [Git proxy documentation](https://git-scm.com/docs/git-config#Documentation/git-config.txt-httpproxy).

## Git over SSH

SSH remotes such as `git@github.com:user/repo.git` do not use HTTP_PROXY. The `--dial` mode provides a bidirectional stream for SSH's ProxyCommand without requiring nc or proxychains:

```sh
git -c core.sshCommand="ssh -o 'ProxyCommand=$PREFIX/bin/wg-cli -c $PREFIX/bin/wg.conf --dial %h:%p'" \
  clone git@github.com:user/repo.git
```

Each SSH connection creates its own temporary tunnel; no outer wg-cli wrapper is needed. Standard output carries only forwarded bytes; diagnostics go to standard error. Adjust quoting if executable or configuration paths contain spaces.

## WireGuard configuration

Supports `[Interface]` and multiple `[Peer]` sections with PrivateKey, Address, DNS, MTU, PublicKey, PresharedKey, Endpoint, AllowedIPs, and PersistentKeepalive. Address retains the host IP from each CIDR.

DNS must contain IP addresses reachable through AllowedIPs and compatible with the configured address families. Search domains are unsupported. Missing DNS causes a validation error rather than falling back to the system resolver for target names.

ListenPort is accepted, but wg-cli always uses an ephemeral local UDP port for concurrent invocations. MTU defaults to 1420 and must be between 1280 and 65535. Unsupported wg-quick fields such as PostUp/PostDown or routing-table settings cause an error; configuration files never execute shell hooks.

Your server must already have the client's public key, tunnel address, and forwarding/NAT configured. AllowedIPs controls which proxy targets are reachable. Full IPv4 egress typically uses `0.0.0.0/0`; IPv6 also needs an IPv6 Address and `::/0`.

`--check` validates syntax and DNS routes only, without testing a handshake. Handshakes occur on demand when traffic starts. Upstream connection attempts time out after 30 seconds.

The proxy listens only on 127.0.0.1 and requires random credentials passed in the child's environment. These credentials do not isolate hostile processes running as the same Termux user. HTTPS retains end-to-end TLS without certificate replacement. wg-cli does not print private keys.

## Process lifecycle

Child exit statuses are preserved. A missing command returns 127; an unexecutable command returns 126; tool errors return 1; usage errors return 2. INT, TERM, and HUP are forwarded to the direct child, which is forcibly terminated after three seconds if needed, then the tunnel is cleaned up.

Use foreground commands. Detached or background descendants are outside lifecycle management and cannot continue using the proxy once the outer command exits. System routes and the parent shell environment remain unchanged.

## Build and test

Use Go 1.25 or newer:

```sh
git clone https://github.com/RainGather/wg-cli.git
cd wg-cli
go test ./...
go vet ./...
sh scripts/build.sh
```

The script cross-compiles an Android arm64 executable into `dist/`, copies license notices, and writes `dist/SHA256SUMS`. It uses `CGO_ENABLED=0` and needs no NDK. The executable requires no additional shared libraries; Android's system loader is used. Android amd64 and 32-bit targets are not currently published.

Integration tests create two real WireGuard devices exchanging encrypted UDP over loopback, with DNS, HTTP, and HTTPS services inside the tunnel. They verify forwarding, authentication, no direct fallback, stdio forwarding, exit statuses, and proxy shutdown. With Git and rclone installed, tests also run real `git ls-remote` and `rclone lsf` commands; otherwise those cases are explicitly skipped. No public VPN credentials are needed. Tests require permission to bind local TCP/UDP sockets.

Local integration tests, race checks, and Android arm64 cross-compilation have passed. Physical Android device testing is still needed with your own configuration.

## License

Project code uses the [MIT License](LICENSE), permitting use, modification, and redistribution. Third-party components retain their own licenses, reproduced in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES). Distribute those license files alongside the executable.

WireGuard support uses the official [wireguard-go user-space network stack](https://git.zx2c4.com/wireguard-go/tree/tun/netstack). Dependency versions and checksums are pinned in `go.mod` and `go.sum`.
