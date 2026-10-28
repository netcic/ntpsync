package server

import (
	"sync/atomic"
	"testing"
	"time"
)

// TestUpstream_Accessors 校验上游地址与间隔的读写及默认值回落。
func TestUpstream_Accessors(t *testing.T) {
	s := New(Config{})

	if got := s.Upstream(); got != "" {
		t.Errorf("默认上游应为空，实际 %q", got)
	}
	s.SetUpstream("ntp.aliyun.com")
	if got := s.Upstream(); got != "ntp.aliyun.com" {
		t.Errorf("设置后应读回新值，实际 %q", got)
	}

	if got := s.UpstreamInterval(); got != DefaultUpstreamInterval {
		t.Errorf("默认间隔应为 %v，实际 %v", DefaultUpstreamInterval, got)
	}
	s.SetUpstreamInterval(30 * time.Minute)
	if got := s.UpstreamInterval(); got != 30*time.Minute {
		t.Errorf("设置后应为 30m，实际 %v", got)
	}
	// 非法值应回落默认，避免 0 间隔导致热循环。
	s.SetUpstreamInterval(0)
	if got := s.UpstreamInterval(); got != DefaultUpstreamInterval {
		t.Errorf("0 应回落默认值，实际 %v", got)
	}
}

// TestUpstreamSync_RuntimeEnable 是本改动的核心回归测试：
// 服务启动时未配置上游，运行期通过 Web 补配后，后台循环应自动开始同步。
// 修复前 StartUpstreamSync 在 upstream=="" 时直接 return，这种情况永远不会同步。
func TestUpstreamSync_RuntimeEnable(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	if err := s.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer s.Stop()

	// 缩短间隔，让轮询粒度落到 1s 下限，测试才跑得动。
	s.SetUpstreamInterval(2 * time.Second)

	var count atomic.Int32
	var seen atomic.Value
	s.StartUpstreamSync(func() {
		count.Add(1)
		seen.Store(s.Upstream())
	})

	// 阶段一：未配置上游，应保持静默。
	time.Sleep(1500 * time.Millisecond)
	if n := count.Load(); n != 0 {
		t.Fatalf("未配置上游时不应触发同步，实际触发 %d 次", n)
	}

	// 阶段二：运行期补配上游，应自动开始同步。
	s.SetUpstream("192.0.2.9")
	waitFor(t, 5*time.Second, func() bool { return count.Load() > 0 })

	if got, _ := seen.Load().(string); got != "192.0.2.9" {
		t.Errorf("同步时应读到新地址，实际读到 %q", got)
	}
	t.Logf("运行期启用上游成功：已同步 %d 次", count.Load())
}

// TestUpstreamSync_AddressChangeTriggersImmediately 校验换地址后
// 不必等满一个同步周期就会触发一次（否则运维改完要干等一小时）。
func TestUpstreamSync_AddressChangeTriggersImmediately(t *testing.T) {
	s := New(Config{
		Listen:           "127.0.0.1:0",
		Upstream:         "192.0.2.9",
		UpstreamInterval: time.Hour,
	})
	// 间隔设很长，若换地址不触发，则测试窗口内不可能出现第二次同步。
	if err := s.Start(); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer s.Stop()

	var count atomic.Int32
	s.StartUpstreamSync(func() { count.Add(1) })

	waitFor(t, 5*time.Second, func() bool { return count.Load() >= 1 })
	first := count.Load()

	s.SetUpstream("192.0.2.10")
	waitFor(t, 5*time.Second, func() bool { return count.Load() > first })
	t.Logf("换地址后立即触发同步（累计 %d 次）", count.Load())
}

// waitFor 轮询等待条件成立，超时则让测试失败。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等待超时（%v）", timeout)
}
