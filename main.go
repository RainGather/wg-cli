package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const version = "0.3.0"

func defaultConfigPath(executable, override string) string {
	if override != "" {
		return override
	}
	return filepath.Join(filepath.Dir(executable), "wg.conf")
}

func proxyEnv(env []string, url string) []string {
	// Clear inherited bypasses and replace conflicting proxies in the child only.
	keys := map[string]bool{"http_proxy": true, "https_proxy": true, "all_proxy": true, "no_proxy": true, "rclone_http_proxy": true, "rclone_ftp_http_proxy": true, "rclone_sftp_http_proxy": true}
	keys["wg_cli_proxy"] = true
	out := make([]string, 0, len(env)+10)
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !keys[strings.ToLower(k)] {
			out = append(out, e)
		}
	}
	for _, k := range []string{"http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "RCLONE_HTTP_PROXY", "RCLONE_FTP_HTTP_PROXY", "RCLONE_SFTP_HTTP_PROXY"} {
		out = append(out, k+"="+url)
	}
	return append(out, "NO_PROXY=", "no_proxy=", "WG_CLI_PROXY="+url)
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("wg-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "wg-cli: locate executable:", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	defaultPath := defaultConfigPath(executable, os.Getenv("WG_CLI_CONFIG"))
	path := fs.String("c", defaultPath, "WireGuard configuration path")
	configure := fs.Bool("config", false, "interactively create or import a WireGuard configuration")
	dialAddress := fs.String("dial", "", "forward stdin/stdout to host:port through WireGuard (SSH ProxyCommand)")
	check := fs.Bool("check", false, "validate configuration without connecting")
	ver := fs.Bool("version", false, "print version")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: wg-cli [-c wg.conf] [--] command [args...]\n       wg-cli [-c wg.conf] --config\n       wg-cli [-c wg.conf] --dial host:port\n       wg-cli [-c wg.conf] --check\n\nRootless, temporary WireGuard HTTP/CONNECT proxy. Only proxy-aware traffic is routed.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *ver {
		fmt.Fprintln(stderr, "wg-cli", version)
		return 0
	}
	if *configure {
		if fs.NArg() != 0 || *check || *dialAddress != "" {
			fmt.Fprintln(stderr, "wg-cli: --config cannot be combined with a command, --check or --dial")
			return 2
		}
		if err := configureFile(*path, os.Stdin, stderr); err != nil {
			fmt.Fprintln(stderr, "wg-cli:", err)
			return 1
		}
		return 0
	}
	if fs.NArg() == 0 && !*check && *dialAddress == "" {
		fs.Usage()
		return 2
	}
	if (*dialAddress != "" || *check) && fs.NArg() != 0 {
		fmt.Fprintln(stderr, "wg-cli: --check/--dial cannot be combined with a command")
		return 2
	}
	if *check && *dialAddress != "" {
		fmt.Fprintln(stderr, "wg-cli: --check and --dial are mutually exclusive")
		return 2
	}
	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintln(stderr, "wg-cli:", err)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "Create a configuration with: wg-cli -c %q --config\n", *path)
		}
		return 1
	}
	c, err := parseConfig(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(stderr, "wg-cli:", err)
		return 1
	}
	if *check {
		fmt.Fprintln(stderr, "WireGuard configuration OK (syntax only; connectivity not tested)")
		return 0
	}
	dial, closeTunnel, err := openTunnel(c)
	if err != nil {
		fmt.Fprintln(stderr, "wg-cli:", err)
		return 1
	}
	defer closeTunnel()
	if *dialAddress != "" {
		if err := stdioDial(context.Background(), dial, *dialAddress, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(stderr, "wg-cli:", err)
			return 1
		}
		return 0
	}
	url, closeProxy, err := startProxy(dial)
	if err != nil {
		fmt.Fprintln(stderr, "wg-cli:", err)
		return 1
	}
	defer closeProxy()
	return runChild(fs.Args(), proxyEnv(os.Environ(), url), stderr)
}

func runChild(args, env []string, stderr io.Writer) int {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr
	signals := make(chan os.Signal, 8)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(stderr, "wg-cli:", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var timer *time.Timer
	var kill <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case sig := <-signals:
			_ = cmd.Process.Signal(sig)
			if timer == nil {
				timer = time.NewTimer(3 * time.Second)
				kill = timer.C
			}
		case <-kill:
			_ = cmd.Process.Kill()
			kill = nil
		case err := <-done:
			if err == nil {
				return 0
			}
			var e *exec.ExitError
			if errors.As(err, &e) {
				if s, ok := e.Sys().(syscall.WaitStatus); ok && s.Signaled() {
					return 128 + int(s.Signal())
				}
				return e.ExitCode()
			}
			fmt.Fprintln(stderr, "wg-cli:", err)
			return 1
		}
	}
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
