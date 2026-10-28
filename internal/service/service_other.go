//go:build !windows

package service

// run 在非 Windows 平台下的实现：无 SCM，直接运行业务循环，
// ctx 监听 SIGINT/SIGTERM 触发取消。
func run(name string, fn RunFunc) error {
	ctx, cancel := newSignalContext()
	defer cancel()
	return fn(ctx)
}
