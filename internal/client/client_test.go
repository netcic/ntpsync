package client

import (
	"strings"
	"testing"
	"time"

	"ntpsync/internal/pal"
	"ntpsync/internal/server"
)

func TestNormalizeAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ntp.ntsc.ac.cn", "ntp.ntsc.ac.cn:123"},
		{"192.168.1.10", "192.168.1.10:123"},
		{"192.168.1.10:123", "192.168.1.10:123"},
		{"127.0.0.1:1123", "127.0.0.1:1123"},
		{"ntp.example.com:1234", "ntp.example.com:1234"},
		{"  ntp.ntsc.ac.cn  ", "ntp.ntsc.ac.cn:123"},
	}
	for _, c := range cases {
		got := normalizeAddr(c.in)
		if got != c.want {
			t.Errorf("normalizeAddr(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// TestCheckAgainstLocalServer 回环端到端：起本地服务端，用客户端 Check 只测不改。
func TestCheckAgainstLocalServer(t *testing.T) {
	s := server.New(server.Config{Listen: "127.0.0.1:0", Stratum: 2})
	if err := s.Start(); err != nil {
		t.Fatalf("服务端 Start 失败: %v", err)
	}
	defer s.Stop()

	addr := s.LocalAddr()

	c, err := New(addr)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	res, err := c.Check()
	if err != nil {
		t.Fatalf("Check 失败: %v", err)
	}
	if res.Offset < -50*time.Millisecond || res.Offset > 50*time.Millisecond {
		t.Errorf("回环 offset 应接近 0，实际 %v", res.Offset)
	}
	if res.Delay < -time.Millisecond {
		t.Errorf("delay 异常: %v", res.Delay)
	}
	t.Logf("offset=%v delay=%v", res.Offset, res.Delay)
}

// TestNewValidation 验证空地址被拒绝。
func TestNewValidation(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("空服务端地址应报错")
	}
}

// TestGuardThreshold 验证护栏阈值判断的边界。
// 不依赖网络：直接校验 maxStep 的三种取值语义。
func TestGuardThreshold(t *testing.T) {
	// 显式阈值：原样返回且启用护栏。
	if d, guarded := (&Client{MaxStep: 5 * time.Second}).maxStep(); d != 5*time.Second || !guarded {
		t.Fatalf("显式阈值应为 5s 且启用护栏，实际 %v guarded=%v", d, guarded)
	}
	// 未设置（0）：回落默认值且启用护栏。
	if d, guarded := (&Client{}).maxStep(); d != DefaultMaxStep || !guarded {
		t.Fatalf("未设置时应回落 %v 且启用护栏，实际 %v guarded=%v", DefaultMaxStep, d, guarded)
	}
	// MaxStepUnlimited：不设阈值。
	if _, guarded := (&Client{MaxStep: MaxStepUnlimited}).maxStep(); guarded {
		t.Fatal("MaxStepUnlimited 时不应启用护栏")
	}
	if MaxStepUnlimited >= 0 {
		t.Fatalf("MaxStepUnlimited 必须为负数，实际 %v", MaxStepUnlimited)
	}
}

// fakePlatform 替代真实系统时钟，使校时测试不会真的改动本机时间。
type fakePlatform struct {
	elevated bool
	now      time.Time
	setTo    time.Time
	setCalls int
}

func (f *fakePlatform) Name() string                        { return "fake" }
func (f *fakePlatform) IsElevated() bool                    { return f.elevated }
func (f *fakePlatform) ReadSystemClock() (time.Time, error) { return f.now, nil }
func (f *fakePlatform) SetSystemTime(t time.Time) error {
	f.setTo = t
	f.setCalls++
	f.now = t
	return nil
}
func (f *fakePlatform) InstallService(pal.ServiceConfig) error { return nil }
func (f *fakePlatform) UninstallService(string) error          { return nil }
func (f *fakePlatform) StartService(string) error              { return nil }
func (f *fakePlatform) StopService(string) error               { return nil }
func (f *fakePlatform) ServiceStatus(string) (string, error)   { return pal.ServiceStatusRunning, nil }
func (f *fakePlatform) ConfigureFirewall(int) error            { return nil }

// newTestClient 构造一个不触网的客户端：偏差由参数决定。
func newTestClient(offset time.Duration, maxStep time.Duration, fp *fakePlatform) *Client {
	return &Client{
		ServerAddr: "127.0.0.1:123",
		Timeout:    time.Second,
		MaxStep:    maxStep,
		platform:   fp,
		query: func(string, time.Duration) (time.Duration, time.Duration, error) {
			return offset, 2 * time.Millisecond, nil
		},
	}
}

// TestSyncUnlimitedAppliesLargeOffset 是本次改动的核心用例：
// 不设阈值时，17 秒这种远超默认 5 秒的偏差也必须真的落地设钟。
func TestSyncUnlimitedAppliesLargeOffset(t *testing.T) {
	base := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	fp := &fakePlatform{elevated: true, now: base}
	res, err := newTestClient(17*time.Second, MaxStepUnlimited, fp).Sync()
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if res.Skipped || !res.Applied {
		t.Fatalf("不设阈值时应直接设钟，实际 Applied=%v Skipped=%v Reason=%q",
			res.Applied, res.Skipped, res.Reason)
	}
	if fp.setCalls != 1 {
		t.Fatalf("应调用一次 SetSystemTime，实际 %d 次", fp.setCalls)
	}
	if want := base.Add(17 * time.Second); !fp.setTo.Equal(want) {
		t.Fatalf("设钟目标应为 %v，实际 %v", want, fp.setTo)
	}
}

// TestSyncDefaultStepStillRejects 确认护栏在客户端默认路径上没被削弱。
func TestSyncDefaultStepStillRejects(t *testing.T) {
	fp := &fakePlatform{elevated: true, now: time.Now()}
	res, err := newTestClient(17*time.Second, 0, fp).Sync()
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if !res.Skipped || res.Applied {
		t.Fatalf("默认 5s 阈值应拦截 17s 偏差，实际 Applied=%v Skipped=%v", res.Applied, res.Skipped)
	}
	if fp.setCalls != 0 {
		t.Fatalf("被拦截时不应设钟，实际调用 %d 次", fp.setCalls)
	}
}

// TestSyncUnlimitedStillNeedsElevation 确认去掉阈值不等于跳过提权检查。
func TestSyncUnlimitedStillNeedsElevation(t *testing.T) {
	fp := &fakePlatform{elevated: false, now: time.Now()}
	res, err := newTestClient(17*time.Second, MaxStepUnlimited, fp).Sync()
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}
	if res.Applied || fp.setCalls != 0 {
		t.Fatal("未提权时不应设钟")
	}
	if !res.Skipped || !strings.Contains(res.Reason, "权限") {
		t.Fatalf("应因权限跳过，实际 Reason=%q", res.Reason)
	}
}

// TestAbs 验证 abs 工具函数。
func TestAbs(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		5 * time.Second:  5 * time.Second,
		-3 * time.Second: 3 * time.Second,
		0:                0,
	}
	for in, want := range cases {
		if got := abs(in); got != want {
			t.Errorf("abs(%v)=%v，应为 %v", in, got, want)
		}
	}
}
