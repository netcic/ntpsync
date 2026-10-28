package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ntpsync/internal/config"
	"ntpsync/internal/pal"
	"ntpsync/internal/web"
)

// tempClientConfig 把配置文件指到临时目录，避免测试污染真实 %ProgramData%。
func tempClientConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ntpsync.json")
	old := clientConfigPath
	clientConfigPath = func() string { return path }
	t.Cleanup(func() { clientConfigPath = old })
	return path
}

// newTestDaemon 构造一个带初始参数的守护设置。
func newTestDaemon() *daemonSettings {
	return &daemonSettings{cur: clientSettings{
		Server:   "192.168.1.10:123",
		Interval: 15 * time.Minute,
		Step:     5 * time.Second,
	}}
}

// TestApplyFromWeb_UpdatesStatusImmediately 回归本次故障：
// 保存配置后，界面读取的状态必须立刻是新值，而不是等下一次校时。
func TestApplyFromWeb_UpdatesStatusImmediately(t *testing.T) {
	tempClientConfig(t)
	st := newTestDaemon()
	h := web.New(nil, &web.Config{
		ServerAddr: "192.168.1.10:123",
		Interval:   "15分钟",
		Step:       "5秒",
	})
	// 模拟「已经校过时」的旧状态。
	h.SetClientStatus(web.ClientStatus{
		ServerAddr: "192.168.1.10:123",
		Interval:   "15分钟",
		Step:       "5秒",
		LastOffset: "3ms",
		LastResult: "NTP 校时 · 已跳过",
		LastAt:     time.Now(),
	})

	cfg := &web.Config{ServerAddr: "10.0.0.9:123", Interval: "30分钟", Step: "10秒"}
	if err := st.applyFromWeb(cfg, h); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	got := h.ClientStatusSnapshot()
	if got == nil {
		t.Fatal("保存后应有状态快照")
	}
	if got.ServerAddr != "10.0.0.9:123" || got.Interval != "30分钟" || got.Step != "10秒" {
		t.Errorf("状态总览应立即反映新参数，实际 %+v", *got)
	}
	// 最近一次校时结果不该被保存动作抹掉。
	if got.LastOffset != "3ms" || got.LastResult != "NTP 校时 · 已跳过" {
		t.Errorf("最近校时结果应保留，实际 %+v", *got)
	}
	// 运行参数本身也要跟着变（否则下一次校时仍按旧间隔）。
	if cur := st.get(); cur.Server != "10.0.0.9:123" || cur.Interval != 30*time.Minute || cur.Step != 10*time.Second {
		t.Errorf("运行参数应已更新，实际 %+v", cur)
	}
}

// TestApplyFromWeb_Persists 保存后应落盘，重启能读回。
func TestApplyFromWeb_Persists(t *testing.T) {
	path := tempClientConfig(t)
	st := newTestDaemon()

	cfg := &web.Config{ServerAddr: "10.0.0.9:123", Interval: "90m", Step: "30秒"}
	if err := st.applyFromWeb(cfg, nil); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}

	saved, err := config.LoadClient(path)
	if err != nil {
		t.Fatalf("读取落盘配置失败: %v", err)
	}
	// 间隔按中文回显落盘，"90m" → "1小时30分钟"。
	if saved.Server != "10.0.0.9:123" || saved.Interval != "1小时30分钟" || saved.Step != "30秒" {
		t.Errorf("落盘内容不符，实际 %+v", *saved)
	}
}

// TestApply_IgnoresEmptyAndInvalid 界面留空/填错时不应把正在跑的参数改坏。
func TestApply_IgnoresEmptyAndInvalid(t *testing.T) {
	st := newTestDaemon()
	before := st.get()

	got := st.apply(&web.Config{ServerAddr: "   ", Interval: "", Step: "abc"})
	if got != before {
		t.Errorf("空值/非法值应保持原参数，实际 %+v", got)
	}
}

// TestApply_NoStatusPushWithoutHandler 没有 Web 界面时（纯守护）不 panic。
func TestApplyFromWeb_NilHandler(t *testing.T) {
	tempClientConfig(t)
	st := newTestDaemon()
	if err := st.applyFromWeb(&web.Config{Interval: "5分钟"}, nil); err != nil {
		t.Fatalf("无 Web 界面时保存不应报错: %v", err)
	}
	if cur := st.get(); cur.Interval != 5*time.Minute {
		t.Errorf("间隔应更新为 5 分钟，实际 %v", cur.Interval)
	}
}

// TestMergeSavedClient 配置优先级：命令行显式参数 > 配置文件 > 默认值。
func TestMergeSavedClient(t *testing.T) {
	path := tempClientConfig(t)
	if err := config.SaveClient(path, &config.ClientConfig{
		Server: "10.0.0.9:123", Interval: "30分钟", Step: "10秒",
	}); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	t.Run("全部取配置文件", func(t *testing.T) {
		s, iv, sp := mergeSavedClient(map[string]bool{}, "", 15*time.Minute, 5*time.Second)
		if s != "10.0.0.9:123" || iv != 30*time.Minute || sp != 10*time.Second {
			t.Errorf("应全部采用配置文件，实际 %s %v %v", s, iv, sp)
		}
	})

	t.Run("命令行显式参数优先", func(t *testing.T) {
		s, iv, sp := mergeSavedClient(map[string]bool{"s": true, "interval": true, "step": true},
			"192.168.1.10:123", time.Minute, 2*time.Second)
		if s != "192.168.1.10:123" || iv != time.Minute || sp != 2*time.Second {
			t.Errorf("显式参数应优先，实际 %s %v %v", s, iv, sp)
		}
	})

	t.Run("混合：只显式给地址", func(t *testing.T) {
		s, iv, sp := mergeSavedClient(map[string]bool{"s": true},
			"192.168.1.10:123", 15*time.Minute, 5*time.Second)
		if s != "192.168.1.10:123" {
			t.Errorf("地址应取命令行，实际 %s", s)
		}
		if iv != 30*time.Minute || sp != 10*time.Second {
			t.Errorf("间隔/阈值应取配置文件，实际 %v %v", iv, sp)
		}
	})
}

// TestMergeSavedClient_NoFile 没有配置文件时按命令行参数跑。
func TestMergeSavedClient_NoFile(t *testing.T) {
	tempClientConfig(t) // 只改路径，不写文件
	s, iv, sp := mergeSavedClient(map[string]bool{}, "127.0.0.1:123", 15*time.Minute, 5*time.Second)
	if s != "127.0.0.1:123" || iv != 15*time.Minute || sp != 5*time.Second {
		t.Errorf("无文件应原样返回，实际 %s %v %v", s, iv, sp)
	}
}

// TestPlanClientInstall_KeepExisting 覆盖「升级时保留旧配置」的分支。
// 校时目标是装机后人工填的，误判会让客户端重装后回到 127.0.0.1:123 而校不到时。
func TestPlanClientInstall_KeepExisting(t *testing.T) {
	want := config.ClientConfig{Server: "127.0.0.1:123", Interval: "15分钟", Step: "5秒"}
	old := &config.ClientConfig{Server: "192.168.55.10:123", Interval: "45分钟", Step: "8秒"}

	got := planClientInstall(want, true, true, old, nil)
	if !got.Keep {
		t.Fatal("要求保留且配置可读时应保留")
	}
	if got.Eff.Server != "192.168.55.10:123" || got.Eff.Interval != "45分钟" || got.Eff.Step != "8秒" {
		t.Errorf("应完整沿用旧配置，实际 %+v", got.Eff)
	}

	if p := planClientInstall(want, false, true, old, nil); p.Keep {
		t.Error("没要求保留时应覆盖")
	} else if p.Eff != want {
		t.Errorf("覆盖时应采用本次参数，实际 %+v", p.Eff)
	}

	if p := planClientInstall(want, true, false, nil, nil); p.Keep {
		t.Error("配置文件不存在时应覆盖")
	}
	if p := planClientInstall(want, true, true, nil, errors.New("bad json")); p.Keep {
		t.Error("配置损坏时应覆盖")
	}
}

// TestPlanClientInstall_PartialOld 旧配置缺字段时逐字段回落，不用空值覆盖。
func TestPlanClientInstall_PartialOld(t *testing.T) {
	want := config.ClientConfig{Server: "127.0.0.1:123", Interval: "15分钟", Step: "5秒"}
	got := planClientInstall(want, true, true, &config.ClientConfig{Server: "10.0.0.9:123"}, nil)
	if !got.Keep {
		t.Fatal("旧配置可读时应保留")
	}
	if got.Eff.Server != "10.0.0.9:123" {
		t.Errorf("旧配置有的字段应沿用，实际 %q", got.Eff.Server)
	}
	if got.Eff.Interval != "15分钟" || got.Eff.Step != "5秒" {
		t.Errorf("旧配置缺的字段应用本次参数补齐，实际 %+v", got.Eff)
	}
}

// fakeServicePlatform 只关心「卸载了哪个服务」，其余方法由 nil 嵌入兜底。
// 卸载路径要提权、会真改系统，所以把服务名决策抽成纯函数单独测。
type fakeServicePlatform struct {
	pal.Platform
	uninstalled []string
	err         error
}

func (f *fakeServicePlatform) UninstallService(name string) error {
	f.uninstalled = append(f.uninstalled, name)
	return f.err
}

// TestRemoveClientServiceUsesClientName 是本轮 bug 的回归护栏：
// 客户端卸载只能操作 SntpClient，绝不能碰服务端的 Sntpserver。
func TestRemoveClientServiceUsesClientName(t *testing.T) {
	fp := &fakeServicePlatform{}
	removed, err := removeClientService(fp)
	if err != nil {
		t.Fatalf("卸载返回错误: %v", err)
	}
	if !removed {
		t.Fatal("真正删掉服务时 removed 应为 true")
	}
	if len(fp.uninstalled) != 1 {
		t.Fatalf("应只操作一个服务，实际 %v", fp.uninstalled)
	}
	if got := fp.uninstalled[0]; got != pal.ClientServiceName {
		t.Fatalf("客户端卸载应传 %q，实际传了 %q", pal.ClientServiceName, got)
	}
	if fp.uninstalled[0] == pal.DefaultServiceName {
		t.Fatal("客户端卸载指向了服务端的服务名，会把服务端服务删掉")
	}
}

// TestRemoveClientServiceNotInstalled 验证「本来就没装」不算失败。
func TestRemoveClientServiceNotInstalled(t *testing.T) {
	fp := &fakeServicePlatform{
		err: fmt.Errorf("%w: %s", pal.ErrServiceNotInstalled, pal.ClientServiceName),
	}
	removed, err := removeClientService(fp)
	if err != nil {
		t.Fatalf("服务未安装不应返回错误: %v", err)
	}
	if removed {
		t.Fatal("服务未安装时 removed 应为 false")
	}
}

// TestCleanConfLine 便携配置文件就一行内容，允许写注释；
// 这里锁住「注释要剥、空内容要报空」这两件事，避免把 `#` 或空行当成服务端地址。
func TestCleanConfLine(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"192.168.1.10:123\n", "192.168.1.10:123"},
		{"  10.0.0.5:123  ", "10.0.0.5:123"},
		{"192.168.1.10:123  # 主时钟", "192.168.1.10:123"},
		{"# 只写了注释", ""},
		{"\n", ""},
	}
	for _, c := range cases {
		if got := cleanConfLine(c.raw); got != c.want {
			t.Errorf("cleanConfLine(%q) = %q，期望 %q", c.raw, got, c.want)
		}
	}
}

// TestPortableConfNames 新文件名优先，旧文件名保留兜底——
// 老站点升级上来不能因为改名就丢配置、退回默认本机地址。
func TestPortableConfNames(t *testing.T) {
	if len(portableConfNames) != 2 {
		t.Fatalf("应有新名+旧名两个候选，实际 %v", portableConfNames)
	}
	if portableConfNames[0] != "SntpClient.conf" {
		t.Errorf("首选应为新名 SntpClient.conf，实际 %q", portableConfNames[0])
	}
	if portableConfNames[1] != "ntpsync.conf" {
		t.Errorf("应保留旧名 ntpsync.conf 兜底，实际 %q", portableConfNames[1])
	}
}

// TestPortableServerFallback 验证便携配置的两个候选文件名真的能用：
// 新版写 SntpClient.conf，老站点留下的是 ntpsync.conf，两者都得读出地址。
func TestPortableServerFallback(t *testing.T) {
	// 只有旧文件：必须读出来，否则升级到新版就退回默认本机地址。
	legacy := t.TempDir()
	if err := os.WriteFile(filepath.Join(legacy, "ntpsync.conf"), []byte("10.0.0.9:123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := portableServer(legacy); got != "10.0.0.9:123" {
		t.Errorf("旧名文件应生效，实际 %q", got)
	}

	// 只剩注释/空文件：应忽略这一份继续找下一个，而不是把注释当地址。
	emptyThenNew := t.TempDir()
	if err := os.WriteFile(filepath.Join(emptyThenNew, "SntpClient.conf"), []byte("# 待配置\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emptyThenNew, "ntpsync.conf"), []byte("10.0.0.8:123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := portableServer(emptyThenNew); got != "10.0.0.8:123" {
		t.Errorf("新文件为空时应回退旧文件，实际 %q", got)
	}

	// 都没有：默认值。
	if got := portableServer(t.TempDir()); got != "127.0.0.1:123" {
		t.Errorf("无文件时应返回默认地址，实际 %q", got)
	}
}
