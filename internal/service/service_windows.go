//go:build windows

package service

import (
	"context"
	"log"

	"golang.org/x/sys/windows/svc"
)

// run 在 Windows 下的实现。
//
// 关键：判断当前是否由 SCM 启动。svc 提供 svc.IsWindowsService()，
// 它检测进程是否运行在非交互式会话（Session 0 无窗口站），
// 能较可靠地区分「sc start 拉起的服务」与「用户双击/命令行前台运行」。
func run(name string, fn RunFunc) error {
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		// 判断失败时按前台运行处理，避免阻断。
		isSvc = false
	}
	if !isSvc {
		// 前台/交互式运行：直接跑业务，监听 Ctrl+C / SIGTERM。
		ctx, cancel := newSignalContext()
		defer cancel()
		return fn(ctx)
	}

	// 被 SCM 拉起：走服务控制分发器。
	return svc.Run(name, &windowsService{fn: fn})
}

// windowsService 实现 svc.Handler，把 SCM 的停止/关机请求转成 ctx 取消。
type windowsService struct {
	fn RunFunc
}

// Execute 是 svc.Handler 的入口。它必须阻塞运行直到收到停止请求。
func (w *windowsService) Execute(args []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 业务循环在独立 goroutine 运行，Execute 阻塞等待 SCM 命令。
	done := make(chan error, 1)
	go func() {
		done <- w.fn(ctx)
	}()

	// 先报告服务启动中，并声明可接受 stop/shutdown。
	status <- svc.Status{State: svc.StartPending}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			// 业务循环结束。
			if err != nil {
				log.Printf("服务运行异常退出: %v", err)
				return false, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// 通知业务循环停止。
				status <- svc.Status{State: svc.StopPending}
				cancel()
				// 等待业务循环优雅退出。
				<-done
				return false, 0
			default:
				log.Printf("收到未知服务命令: %d", c.Cmd)
			}
		}
	}
}
