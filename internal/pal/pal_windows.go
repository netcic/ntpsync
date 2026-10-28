//go:build windows

package pal

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// winPlatform 实现 Windows 下的 Platform 接口。
//
// 它不保存「当前服务名」这类状态：服务名一律由调用方显式传入。
// 早期版本在 InstallService 里记住名字、其余方法回落 ntpsyncd，
// 于是新进程里执行 `ntpsync uninstall` 会把服务端的 ntpsyncd 删掉。
type winPlatform struct{}

// NewPlatform 返回当前平台实现（每个平台文件各自定义，编译时仅其一生效）。
func NewPlatform() Platform { return &winPlatform{} }

func (p *winPlatform) Name() string { return "windows" }

// IsElevated 通过 TokenElevation 判断当前进程是否被提升（管理员）。
func (p *winPlatform) IsElevated() bool {
	k := syscall.NewLazyDLL("kernel32.dll")
	a := syscall.NewLazyDLL("advapi32.dll")
	getCurrentProcess := k.NewProc("GetCurrentProcess")
	openProcessToken := a.NewProc("OpenProcessToken")
	getTokenInformation := a.NewProc("GetTokenInformation")
	closeHandle := k.NewProc("CloseHandle")

	hProc, _, _ := getCurrentProcess.Call()
	if hProc == 0 {
		return false
	}
	var hToken uintptr
	r, _, _ := openProcessToken.Call(hProc, 0x8 /*TOKEN_QUERY*/, uintptr(unsafe.Pointer(&hToken)))
	if r == 0 {
		return false
	}
	defer closeHandle.Call(hToken)

	var elevation uint32
	var retLen uint32
	r, _, _ = getTokenInformation.Call(hToken, 0x14 /*TokenElevation*/, uintptr(unsafe.Pointer(&elevation)), uintptr(unsafe.Sizeof(elevation)), uintptr(unsafe.Pointer(&retLen)))
	if r == 0 {
		return false
	}
	return elevation != 0
}

// ReadSystemClock 读取系统时钟（UTC）。
func (p *winPlatform) ReadSystemClock() (time.Time, error) {
	return time.Now().UTC(), nil
}

// systemTime 对应 Windows API 的 SYSTEMTIME 结构（字段均为 uint16）。
type systemTime struct {
	Year         uint16
	Month        uint16
	DayOfWeek    uint16
	Day          uint16
	Hour         uint16
	Minute       uint16
	Second       uint16
	Milliseconds uint16
}

// systemTimeFrom 把时刻转换为 Windows SYSTEMTIME。
//
// 必须取 UTC 分量：kernel32 的 SetSystemTime 按 UTC 解释入参（接受本地时间的是
// SetLocalTime）。若误传本地分量，在 UTC+8 环境下每设一次钟就把系统时间整体
// 拨快 8 小时，且下一次测量会得到 -8h 的偏差、再设一次又加 8h——形成永久偏移。
func systemTimeFrom(t time.Time) systemTime {
	u := t.UTC()
	return systemTime{
		Year:         uint16(u.Year()),
		Month:        uint16(u.Month()),
		Day:          uint16(u.Day()),
		Hour:         uint16(u.Hour()),
		Minute:       uint16(u.Minute()),
		Second:       uint16(u.Second()),
		Milliseconds: uint16(u.Nanosecond() / 1e6),
	}
}

// SetSystemTime 将系统时钟设为 t（UTC）。
func (p *winPlatform) SetSystemTime(t time.Time) error {
	if !p.IsElevated() {
		return ErrNotElevated
	}
	st := systemTimeFrom(t)
	k := syscall.NewLazyDLL("kernel32.dll")
	proc := k.NewProc("SetSystemTime")
	r, _, err := proc.Call(uintptr(unsafe.Pointer(&st)))
	if r == 0 {
		if err == nil {
			err = fmt.Errorf("unknown error")
		}
		return fmt.Errorf("pal: SetSystemTime 失败: %w", err)
	}
	// 回读校验：参数语义用错时会"成功"但整体偏移一个时区，必须显式暴露。
	return verifyClockSet(t)
}

// InstallService 通过 sc.exe 注册 Windows 服务并设为自启。
func (p *winPlatform) InstallService(cfg ServiceConfig) error {
	if err := checkServiceName(cfg.Name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	displayName := cfg.DisplayName
	if displayName == "" {
		displayName = cfg.Name
	}
	startType := cfg.StartType
	if startType == "" {
		startType = "auto"
	}
	bin := fmt.Sprintf("\"%s\"", cfg.BinPath)
	if len(cfg.Args) > 0 {
		bin += " " + strings.Join(cfg.Args, " ")
	}
	out, err := exec.Command("sc.exe", "create", cfg.Name,
		"binPath=", bin, "start=", startType, "DisplayName=", displayName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pal: sc create 失败: %v: %s", err, string(out))
	}
	// 写描述（可选，失败不阻断）。
	if cfg.Description != "" {
		exec.Command("sc.exe", "description", cfg.Name, cfg.Description).Run()
	}
	return nil
}

// scOutput 执行 sc.exe 并返回合并输出；失败时把 SCM 的退出码一并带出。
//
// 退出码是判断「服务不存在」等状态最可靠的依据：sc.exe 的提示文本是本地化的，
// 中文系统上卸载一个未安装的服务会输出「指定的服务并未安装」，按英文串匹配必漏。
func scOutput(args ...string) (string, int, error) {
	out, err := exec.Command("sc.exe", args...).CombinedOutput()
	text := string(out)
	if err == nil {
		return text, 0, nil
	}
	code := 1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	return text, code, fmt.Errorf("pal: sc %s 失败: %v: %s",
		strings.Join(args, " "), err, strings.TrimSpace(text))
}

// SCM 状态码（WinError）。用退出码判断，避免依赖本地化的提示文本。
const (
	scErrServiceNotInstalled = 1060 // ERROR_SERVICE_DOES_NOT_EXIST
	scErrServiceNotActive    = 1062 // ERROR_SERVICE_NOT_ACTIVE
)

// waitStopped 轮询服务状态，直到不再是 RUNNING 或超时。
// sc stop 是异步的：发完命令立刻 delete 会落到「标记删除待定、进程继续跑」。
func waitStopped(name string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("sc.exe", "query", name).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "RUNNING") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// UninstallService 停止并删除名为 name 的服务。
//
// name 由调用方显式传入：客户端必须传 ClientServiceName，
// 否则会把同一台机器上服务端的 ntpsyncd 一起删掉（历史 bug）。
func (p *winPlatform) UninstallService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	// 先停后删，与 Linux 实现保持一致。
	if _, err := exec.Command("sc.exe", "stop", name).CombinedOutput(); err == nil {
		waitStopped(name, 5*time.Second)
	}
	if _, code, err := scOutput("delete", name); err != nil {
		if code == scErrServiceNotInstalled {
			return fmt.Errorf("%w: %s", ErrServiceNotInstalled, name)
		}
		return err
	}
	return nil
}

// StartService 启动名为 name 的服务。
func (p *winPlatform) StartService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	_, _, err := scOutput("start", name)
	return err
}

// StopService 停止名为 name 的服务。
// 服务本来就处于停止状态（1062）视为成功——调用方要的结果已经达成。
func (p *winPlatform) StopService(name string) error {
	if err := checkServiceName(name); err != nil {
		return err
	}
	if !p.IsElevated() {
		return ErrNotElevated
	}
	_, code, err := scOutput("stop", name)
	if err != nil && code == scErrServiceNotActive {
		return nil
	}
	return err
}

// ConfigureFirewall 通过 netsh 放行指定 UDP 端口入站（服务端授时）。
func (p *winPlatform) ConfigureFirewall(udpPort int) error {
	if !p.IsElevated() {
		return ErrNotElevated
	}
	rule := fmt.Sprintf("NTP 授时服务 (UDP %d)", udpPort)
	// 先删同名旧规则（避免重复），失败忽略；再添加。
	_ = exec.Command("netsh", "advfirewall", "firewall", "delete", "rule",
		"name="+rule).Run()
	out, err := exec.Command("netsh", "advfirewall", "firewall", "add", "rule",
		"name="+rule, "dir=in", "action=allow", "protocol=UDP",
		fmt.Sprintf("localport=%d", udpPort)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("pal: netsh 放行端口失败: %v: %s", err, string(out))
	}
	return nil
}

// ServiceStatus 查询名为 name 的服务状态，返回 ServiceStatus* 常量。
func (p *winPlatform) ServiceStatus(name string) (string, error) {
	if err := checkServiceName(name); err != nil {
		return "", err
	}
	out, code, err := scOutput("query", name)
	if err != nil {
		if code == scErrServiceNotInstalled {
			return ServiceStatusNotInstalled, nil
		}
		return "", err
	}
	switch {
	case strings.Contains(out, "RUNNING"):
		return ServiceStatusRunning, nil
	case strings.Contains(out, "STOPPED"):
		return ServiceStatusStopped, nil
	default:
		return ServiceStatusStopped, nil
	}
}
