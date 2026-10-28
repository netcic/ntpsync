//go:build linux

package pal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// linuxPlatform 实现 Linux / 国产 OS 下的 Platform 接口。
// 国产 OS（UOS、麒麟等）同 Linux 内核，行为一致；差异仅在 init 系统。
//
// 与 Windows 实现一样不保存「当前服务名」：服务名由调用方显式传入，
// 避免客户端命令误操作服务端的 ntpsyncd。
type linuxPlatform struct{}

// NewPlatform 返回当前平台实现（每个平台文件各自定义，编译时仅其一生效）。
func NewPlatform() Platform { return &linuxPlatform{} }

func (p *linuxPlatform) Name() string { return "linux" }

// IsElevated 判断是否为 root（euid==0）。
func (p *linuxPlatform) IsElevated() bool {
	return os.Geteuid() == 0
}

// ReadSystemClock 读取系统时钟（UTC）。
func (p *linuxPlatform) ReadSystemClock() (time.Time, error) {
	return time.Now().UTC(), nil
}

// SetSystemTime 通过 clock_settime(CLOCK_REALTIME, ...) 设置系统时钟（UTC）。
func (p *linuxPlatform) SetSystemTime(t time.Time) error {
	if !p.IsElevated() {
		return ErrNotElevated
	}
	t = t.UTC()
	ts := syscall.Timespec{
		Sec:  t.Unix(),
		Nsec: int64(t.Nanosecond()),
	}
	// SYS_CLOCK_SETTIME: int clock_settime(clockid_t, const struct timespec*)。
	// CLOCK_REALTIME == 0。
	_, _, errno := syscall.Syscall(syscall.SYS_CLOCK_SETTIME, 0, uintptr(unsafe.Pointer(&ts)), 0)
	if errno != 0 {
		return fmt.Errorf("pal: clock_settime 失败: %w", errno)
	}
	// 回读校验：防止「入参语义用错但调用成功」导致的静默偏移。
	return verifyClockSet(t)
}

// detectInitSystem 探测 init 系统：systemd / sysv / unknown。
func detectInitSystem() string {
	if _, err := exec.LookPath("systemctl"); err == nil {
		return "systemd"
	}
	if _, err := exec.LookPath("update-rc.d"); err == nil {
		return "sysv"
	}
	if _, err := exec.LookPath("chkconfig"); err == nil {
		return "sysv"
	}
	return "unknown"
}

// systemdUnit 生成 systemd service 单元内容。
func systemdUnit(cfg ServiceConfig) string {
	desc := cfg.Description
	if desc == "" {
		desc = cfg.Name
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=%s %s
Restart=on-failure
RestartSec=5
# 校时需要改系统时钟，确保能力在位
AmbientCapabilities=CAP_SYS_TIME

[Install]
WantedBy=multi-user.target
`, desc, cfg.BinPath, strings.Join(cfg.Args, " "))
}

// sysvScript 生成 SysVinit 风格 init.d 脚本（start-stop-daemon 方案）。
func sysvScript(cfg ServiceConfig) string {
	desc := cfg.Description
	if desc == "" {
		desc = cfg.Name
	}
	return fmt.Sprintf(`#!/bin/sh
### BEGIN INIT INFO
# Provides:          %s
# Required-Start:    $network $local_fs
# Required-Stop:     $network $local_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Description:       %s
### END INIT INFO
NAME=%s
DAEMON=%s
ARGS="%s"
case "$1" in
  start)   start-stop-daemon --start --quiet --exec "$DAEMON" -- $ARGS ;;
  stop)    start-stop-daemon --stop  --quiet --name "$NAME" ;;
  restart) $0 stop; sleep 1; $0 start ;;
  *) echo "Usage: $0 {start|stop|restart}"; exit 1 ;;
esac
exit 0
`, cfg.Name, desc, cfg.Name, cfg.BinPath, strings.Join(cfg.Args, " "))
}

// InstallService 注册为系统服务并配置开机自启。
// 主路径：systemd（unit 写入 /etc/systemd/system，enable+start）。
// 兜底路径：SysVinit（init.d 脚本 + update-rc.d/chkconfig）。
func (p *linuxPlatform) InstallService(cfg ServiceConfig) error {
	if err := checkServiceName(cfg.Name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	startType := cfg.StartType
	if startType == "" {
		startType = "auto"
	}

	switch detectInitSystem() {
	case "systemd":
		unitPath := filepath.Join("/etc/systemd/system", cfg.Name+".service")
		if err := os.WriteFile(unitPath, []byte(systemdUnit(cfg)), 0644); err != nil {
			return fmt.Errorf("pal: 写入 unit 文件失败: %w", err)
		}
		run("systemctl", "daemon-reload")
		if out, err := exec.Command("systemctl", "enable", cfg.Name).CombinedOutput(); err != nil {
			return fmt.Errorf("pal: systemctl enable 失败: %v: %s", err, string(out))
		}
		if startType == "auto" {
			if out, err := exec.Command("systemctl", "start", cfg.Name).CombinedOutput(); err != nil {
				return fmt.Errorf("pal: systemctl start 失败: %v: %s", err, string(out))
			}
		}
		return nil
	case "sysv":
		scriptPath := filepath.Join("/etc/init.d", cfg.Name)
		if err := os.WriteFile(scriptPath, []byte(sysvScript(cfg)), 0755); err != nil {
			return fmt.Errorf("pal: 写入 init.d 脚本失败: %w", err)
		}
		// 优先 Debian 系 update-rc.d，其次 RHEL 系 chkconfig
		if _, err := exec.LookPath("update-rc.d"); err == nil {
			run("update-rc.d", cfg.Name, "defaults")
		} else if _, err := exec.LookPath("chkconfig"); err == nil {
			run("chkconfig", "--add", cfg.Name)
			if startType == "auto" {
				run("chkconfig", cfg.Name, "on")
			}
		} else {
			return fmt.Errorf("%w: 未找到 update-rc.d / chkconfig", ErrUnsupported)
		}
		if startType == "auto" {
			run("service", cfg.Name, "start")
		}
		return nil
	default:
		return fmt.Errorf("%w: 未知 init 系统，无法安装服务", ErrUnsupported)
	}
}

// UninstallService 停止并卸载名为 name 的系统服务。
//
// name 由调用方显式传入（服务端 DefaultServiceName / 客户端 ClientServiceName），
// 不能内部兜底：同一台机器上可能同时装了服务端与客户端。
func (p *linuxPlatform) UninstallService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	switch detectInitSystem() {
	case "systemd":
		unit := filepath.Join("/etc/systemd/system", name+".service")
		if _, err := os.Stat(unit); err != nil {
			return fmt.Errorf("%w: %s", ErrServiceNotInstalled, name)
		}
		run("systemctl", "stop", name)
		run("systemctl", "disable", name)
		os.Remove(unit)
		run("systemctl", "daemon-reload")
		return nil
	case "sysv":
		script := filepath.Join("/etc/init.d", name)
		if _, err := os.Stat(script); err != nil {
			return fmt.Errorf("%w: %s", ErrServiceNotInstalled, name)
		}
		run("service", name, "stop")
		if _, err := exec.LookPath("update-rc.d"); err == nil {
			run("update-rc.d", "-f", name, "remove")
		} else if _, err := exec.LookPath("chkconfig"); err == nil {
			run("chkconfig", "--del", name)
		}
		os.Remove(script)
		return nil
	default:
		return fmt.Errorf("%w: 未知 init 系统，无法卸载服务", ErrUnsupported)
	}
}

// StartService 启动名为 name 的服务。
func (p *linuxPlatform) StartService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	switch detectInitSystem() {
	case "systemd":
		return runErr("systemctl", "start", name)
	case "sysv":
		return runErr("service", name, "start")
	default:
		return fmt.Errorf("%w: 未知 init 系统", ErrUnsupported)
	}
}

// StopService 停止名为 name 的服务。
func (p *linuxPlatform) StopService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	switch detectInitSystem() {
	case "systemd":
		return runErr("systemctl", "stop", name)
	case "sysv":
		return runErr("service", name, "stop")
	default:
		return fmt.Errorf("%w: 未知 init 系统", ErrUnsupported)
	}
}

// ServiceStatus 返回名为 name 的服务状态（ServiceStatus* 常量）。
func (p *linuxPlatform) ServiceStatus(name string) (string, error) {
	if err := checkServiceName(name); err != nil {
		return "", err
	}
	switch detectInitSystem() {
	case "systemd":
		out, _ := exec.Command("systemctl", "is-active", name).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if s == "active" {
			return ServiceStatusRunning, nil
		}
		if s == "inactive" || s == "failed" {
			return ServiceStatusStopped, nil
		}
		// 其它（如 "unknown"）视为未安装
		return ServiceStatusNotInstalled, nil
	case "sysv":
		// 用 pidof / pgrep 做轻量探测
		if _, err := exec.LookPath("pgrep"); err == nil {
			if out, _ := exec.Command("pgrep", "-f", name).Output(); len(strings.TrimSpace(string(out))) > 0 {
				return ServiceStatusRunning, nil
			}
			return ServiceStatusStopped, nil
		}
		return ServiceStatusStopped, nil
	default:
		return "", fmt.Errorf("%w: 未知 init 系统", ErrUnsupported)
	}
}

// ConfigureFirewall 放行指定 UDP 端口入站（服务端授时）。
// 优先 firewalld，其次 ufw；均不可用时返回 ErrUnsupported。
func (p *linuxPlatform) ConfigureFirewall(udpPort int) error {
	if !p.IsElevated() {
		return ErrNotElevated
	}
	port := fmt.Sprintf("%d/udp", udpPort)
	_, fwErr := exec.LookPath("firewall-cmd")
	_, ufwErr := exec.LookPath("ufw")
	switch {
	case fwErr == nil:
		if out, err := exec.Command("firewall-cmd", "--permanent", "--add-port", port).CombinedOutput(); err != nil {
			return fmt.Errorf("pal: firewall-cmd 放行失败: %v: %s", err, string(out))
		}
		run("firewall-cmd", "--reload")
		return nil
	case ufwErr == nil:
		if out, err := exec.Command("ufw", "allow", port).CombinedOutput(); err != nil {
			return fmt.Errorf("pal: ufw 放行失败: %v: %s", err, string(out))
		}
		return nil
	default:
		return fmt.Errorf("%w: 未找到 firewall-cmd/ufw，请手动放行 UDP %d", ErrUnsupported, udpPort)
	}
}

// run 执行命令并忽略错误（用于 best-effort 的辅助步骤）。
func run(name string, args ...string) {
	_ = exec.Command(name, args...).Run()
}

// runErr 执行命令，返回错误（用于关键步骤）。
func runErr(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pal: %s %v 失败: %v: %s", name, args, err, string(out))
	}
	return nil
}
