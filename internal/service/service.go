// Package service 提供「以系统服务身份运行」的跨平台适配。
//
// 设计目标：让 ntpsyncd / ntpsync 既能在前台交互式运行（check/sync/daemon），
// 也能在被 Windows SCM 拉起时正确报告 RUNNING/STOPPED，从而让
// `sc start/stop/query` 正常工作（否则 sc start 会一直阻塞等待超时）。
//
// Windows：调用 golang.org/x/sys/windows/svc 接管服务控制分发器，
// 向 SCM 报告服务状态，并把「停止」请求通过 stopCh 转发给业务循环。
// 非 Windows（Linux 等）：无 SCM，直接同步运行 handler，stopCh 监听 SIGTERM/SIGINT。
package service

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// RunFunc 是业务主循环。它应当阻塞运行直到 ctx 被取消（收到停止请求），
// 然后优雅清理并返回。返回 nil 表示正常退出，非 nil 表示异常退出。
type RunFunc func(ctx context.Context) error

// Run 以服务身份运行 fn。
//
//   - Windows 下若被 SCM 拉起（环境变量或交互式标志），走 svc.Run 接管控制；
//     否则退化回前台直接运行（便于手动调试）。
//   - 非 Windows 下直接运行，并监听 SIGINT/SIGTERM 触发取消。
//
// 交互式前台运行时，ctx 也会监听 Ctrl+C / SIGTERM，保证行为一致。
func Run(name string, fn RunFunc) error {
	return run(name, fn)
}

// newSignalContext 构造一个监听系统信号并自动 cancel 的 context。
// 用于非 Windows 平台及 Windows 前台退化路径。
func newSignalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(ch)
	}()
	return ctx, cancel
}
