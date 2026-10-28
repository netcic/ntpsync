// Package pal 提供平台抽象层（Platform Abstraction Layer）：
// 屏蔽 Windows / Linux / 国产 OS 在「读系统时钟、改系统时钟（需提权）、
// 安装/启停系统服务」上的差异，向上层（服务端守护、客户端校时）提供统一接口。
//
// 仅依赖标准库，便于离线静态编译与跨平台（x86_64 / arm64）分发。
//
// 平台相关实现放在带 build tag 的文件中：
//   - pal_windows.go  //go:build windows
//   - pal_linux.go    //go:build linux
//
// 本文件只放公共接口、错误与结构定义。
package pal

import (
	"errors"
	"fmt"
	"time"
)

// ErrNotElevated 在需要管理员/root 权限但未提权时由各实现返回。
var ErrNotElevated = errors.New("pal: 需要管理员/root 权限才能修改系统时钟或管理服务")

// ErrUnsupported 表示当前平台/环境不支持该操作（如 SysVinit 兜底缺失）。
var ErrUnsupported = errors.New("pal: 当前平台/环境不支持该操作")

// clockSetTolerance 为设钟后回读校验允许的误差。
// 设钟与回读之间会消耗一点时间，也可能有别的对时源同时动作，故留 2 秒余量；
// 一个时区（≥30 分钟）或系统时间被整体拨错这类问题远超该量级，能被可靠捕获。
const clockSetTolerance = 2 * time.Second

// verifyClockSet 在设钟成功后立刻回读系统时钟，确认确实落在目标附近。
//
// 为什么需要它：SetSystemTime/clock_settime 的入参语义（UTC 还是本地时间）
// 一旦用错，调用会「成功返回」但把时钟整体偏移一个时区——上层只看到"设钟成功"，
// 没有任何迹象表明时间已经错了。这道回读校验把静默偏移变成显式错误。
func verifyClockSet(target time.Time) error {
	back := time.Now()
	diff := back.Sub(target)
	if diff < 0 {
		diff = -diff
	}
	if diff > clockSetTolerance {
		return fmt.Errorf("pal: 设钟校验失败：期望 %s，回读为 %s（相差 %s）",
			target.UTC().Format(time.RFC3339), back.UTC().Format(time.RFC3339), back.Sub(target))
	}
	return nil
}

// Platform 抽象各 OS 的时钟与服务管理操作。
type Platform interface {
	// Name 返回平台标识："windows" / "linux"。
	Name() string
	// IsElevated 报告当前进程是否拥有修改系统时钟/装服务的权限。
	IsElevated() bool
	// ReadSystemClock 读取系统时钟（返回 UTC）。
	ReadSystemClock() (time.Time, error)
	// SetSystemTime 将系统时钟设为 t（UTC）。需要提权，否则返回 ErrNotElevated。
	SetSystemTime(t time.Time) error

	// InstallService 将本程序注册为系统服务并配置开机自启。需要提权。
	InstallService(cfg ServiceConfig) error
	// UninstallService 停止并卸载名为 name 的系统服务。需要提权。
	// name 必须由调用方显式给出（服务端 DefaultServiceName / 客户端 ClientServiceName）：
	// 早期版本省略该参数、内部兜底成 ntpsyncd，导致客户端执行 uninstall 时
	// 把同一台机器上的服务端服务一起删掉。
	UninstallService(name string) error
	// StartService 启动名为 name 的系统服务。需要提权。
	StartService(name string) error
	// StopService 停止名为 name 的系统服务。需要提权。
	StopService(name string) error
	// ServiceStatus 返回名为 name 的系统服务状态，取值见 ServiceStatus* 常量。
	ServiceStatus(name string) (string, error)

	// ConfigureFirewall 放行指定 UDP 端口的入站流量（服务端授时用）。需要提权。
	// 未提权或平台不支持时返回错误（ErrNotElevated / ErrUnsupported）。
	ConfigureFirewall(udpPort int) error
}

// ErrServiceNotInstalled 表示目标服务未注册。
// 卸载场景下它不是一个错误（目标状态已达成），调用方可据此打印友好提示而非报错退出。
var ErrServiceNotInstalled = errors.New("pal: 服务未安装")

// 服务状态取值。
const (
	ServiceStatusRunning      = "running"
	ServiceStatusStopped      = "stopped"
	ServiceStatusNotInstalled = "not_installed"
)

// 系统服务名。两个程序各自只管理自己那一个服务，名字不可混用。
const (
	// DefaultServiceName 服务端（Sntpserver）注册的服务名。
	DefaultServiceName = "Sntpserver"
	// ClientServiceName 客户端（SntpClient）注册的服务名。
	ClientServiceName = "SntpClient"
)

// checkServiceName 拦截空服务名。
// 空串交给 sc.exe/systemctl 会变成语法错误，更糟的是在「有默认名兜底」的旧实现里
// 会静默操作到别人的服务上——所以宁可直接拒绝。
func checkServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("pal: 服务名为空，拒绝操作（服务端用 DefaultServiceName，客户端用 ClientServiceName）")
	}
	return nil
}

// ServiceConfig 描述一个系统服务的安装参数。
type ServiceConfig struct {
	// Name 服务名（无空格的内部标识），如 "Sntpserver"。
	Name string
	// DisplayName 显示名（可含空格），留空则等同 Name。
	DisplayName string
	// Description 描述，写入服务元数据。
	Description string
	// BinPath 可执行文件路径（绝对路径最佳）。
	BinPath string
	// Args 启动参数，如 ["--server","192.168.1.10"]。
	Args []string
	// StartType 开机行为："auto"（默认）/ "manual" / "disabled"。
	StartType string
}
