package pal

import (
	"os"
	"testing"
	"time"
)

// TestNewPlatform 验证平台实现可被构造且标识正确。
func TestNewPlatform(t *testing.T) {
	p := NewPlatform()
	if p == nil {
		t.Fatal("NewPlatform 返回 nil")
	}
	name := p.Name()
	if name != "windows" && name != "linux" {
		t.Fatalf("Name 应为 windows/linux，实际 %q", name)
	}
	t.Logf("platform=%s elevated=%v", name, p.IsElevated())
}

// TestReadSystemClock 验证能读到合理的系统时钟（UTC）。
func TestReadSystemClock(t *testing.T) {
	p := NewPlatform()
	now, err := p.ReadSystemClock()
	if err != nil {
		t.Fatalf("ReadSystemClock 失败: %v", err)
	}
	if now.Year() < 2020 {
		t.Fatalf("读到的系统时间异常: %v", now)
	}
	if now.Location() != time.UTC {
		t.Fatalf("ReadSystemClock 应返回 UTC，实际 %v", now.Location())
	}
}

// TestSetSystemTimeSafe 验证设钟权限护栏：
//   - 未提权时必须返回错误，且不修改系统时钟；
//   - 提权且显式开启 NTP_TEST_SETCLOCK 时，才真正调用（设为当前时间，几乎无变化）。
func TestSetSystemTimeSafe(t *testing.T) {
	p := NewPlatform()

	if !p.IsElevated() {
		// 安全路径：未提权不应改钟，必须报错。
		if err := p.SetSystemTime(time.Now().Add(time.Hour)); err == nil {
			t.Fatal("未提权时 SetSystemTime 应返回错误（且不应修改系统时钟）")
		}
		t.Log("未提权：已确认设钟被拦截，未修改系统时钟")
		return
	}

	// 提权进程：默认不主动改钟，避免影响运行环境。
	if os.Getenv("NTP_TEST_SETCLOCK") != "1" {
		t.Log("提权进程但未设 NTP_TEST_SETCLOCK=1，跳过真实设钟，避免修改系统时钟")
		return
	}
	// 显式允许：设为当前时间（基本无偏移），验证调用链路可用。
	if err := p.SetSystemTime(time.Now().UTC()); err != nil {
		t.Fatalf("提权下 SetSystemTime 失败: %v", err)
	}
}

// TestServiceManagementSafe 验证服务管理接口可用；
// 真实装/启/停/卸仅在 NTP_TEST_SERVICE=1 时执行，默认 skip，避免污染系统。
func TestServiceManagementSafe(t *testing.T) {
	if os.Getenv("NTP_TEST_SERVICE") != "1" {
		t.Skip("默认跳过真实服务管理测试；设 NTP_TEST_SERVICE=1（需提权）可启用")
	}
	p := NewPlatform()
	cfg := ServiceConfig{
		Name:        "ntpsyncd_test",
		DisplayName: "NTP Sync Test",
		Description: "测试用 NTP 授时服务",
		BinPath:     "/usr/local/bin/Sntpserver",
		Args:        []string{"--server", "127.0.0.1"},
		StartType:   "manual",
	}
	if err := p.InstallService(cfg); err != nil {
		t.Fatalf("InstallService 失败: %v", err)
	}
	if _, err := p.ServiceStatus(cfg.Name); err != nil {
		t.Fatalf("ServiceStatus 失败: %v", err)
	}
	if err := p.StartService(cfg.Name); err != nil {
		t.Logf("StartService 失败（可能 bin 不存在）: %v", err)
	}
	if err := p.StopService(cfg.Name); err != nil {
		t.Logf("StopService 失败: %v", err)
	}
	if err := p.UninstallService(cfg.Name); err != nil {
		t.Fatalf("UninstallService 失败: %v", err)
	}
}

// TestServiceNames 锁定两个程序的专属服务名，且必须互不相同。
//
// 这是「客户端卸载把服务端服务一起删掉」那个 bug 的根因护栏：
// 只要两个名字不相等，任何一方的命令都不可能删掉对方的服务。
func TestServiceNames(t *testing.T) {
	if DefaultServiceName != "Sntpserver" {
		t.Errorf("服务端服务名应为 Sntpserver，实际 %q", DefaultServiceName)
	}
	if ClientServiceName != "SntpClient" {
		t.Errorf("客户端服务名应为 SntpClient，实际 %q", ClientServiceName)
	}
	if DefaultServiceName == ClientServiceName {
		t.Fatalf("服务端与客户端服务名不能相同：%q", DefaultServiceName)
	}
}

// TestCheckServiceName 空服务名必须被拒绝，绝不能回落成任何默认值——
// 旧实现正是靠「空名字兜底 ntpsyncd」把服务端的服务删除的。
func TestCheckServiceName(t *testing.T) {
	if err := checkServiceName(""); err == nil {
		t.Fatal("空服务名应返回错误")
	}
	if err := checkServiceName(ClientServiceName); err != nil {
		t.Errorf("合法服务名不应报错: %v", err)
	}
}

// stubPlatform 只实现 CleanupLegacyService 用得到的那一个方法。
// 嵌入 Platform 接口即可：未覆盖的方法保持 nil，测试里不会调到，
// 比为了一个用例去实现十几个接口方法划算得多。
type stubPlatform struct {
	Platform
	calls        []string
	uninstallErr error
}

func (s *stubPlatform) UninstallService(name string) error {
	s.calls = append(s.calls, name)
	return s.uninstallErr
}

// TestLegacyServiceNamesDifferFromCurrent 守住一条底线：旧服务名绝不能与现役服务名
// 相同。一旦相同，升级清理就会把刚装好的新服务自己删掉。
func TestLegacyServiceNamesDifferFromCurrent(t *testing.T) {
	if LegacyServerServiceName == DefaultServiceName {
		t.Fatalf("旧版服务端服务名与现役同名（%s），升级清理会误删新服务", DefaultServiceName)
	}
	if LegacyClientServiceName == ClientServiceName {
		t.Fatalf("旧版客户端服务名与现役同名（%s），升级清理会误删新服务", ClientServiceName)
	}
	if LegacyServerServiceName == ClientServiceName || LegacyClientServiceName == DefaultServiceName {
		t.Fatalf("新旧服务名发生交叉：server=%s/%s client=%s/%s",
			LegacyServerServiceName, DefaultServiceName, LegacyClientServiceName, ClientServiceName)
	}
}

// TestCleanupLegacyService 覆盖升级清理的三种结局：删掉了 / 本来就没有 / 真出错。
func TestCleanupLegacyService(t *testing.T) {
	cases := []struct {
		name      string
		legacy    string
		err       error
		wantDel   bool
		wantErr   bool
		wantCalls int
	}{
		{name: "旧服务存在并被清掉", legacy: "ntpsyncd", err: nil, wantDel: true, wantCalls: 1},
		{name: "旧服务不存在不算错", legacy: "ntpsyncd", err: ErrServiceNotInstalled, wantDel: false, wantCalls: 1},
		{name: "权限不足要如实上报", legacy: "ntpsyncd", err: ErrNotElevated, wantDel: false, wantErr: true, wantCalls: 1},
		{name: "空名直接跳过不调用", legacy: "", err: nil, wantDel: false, wantCalls: 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := &stubPlatform{uninstallErr: c.err}
			got, err := CleanupLegacyService(st, c.legacy)
			if c.wantErr && err == nil {
				t.Fatalf("期望返回错误，实际为 nil")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("期望无错误，实际: %v", err)
			}
			if got != c.wantDel {
				t.Fatalf("删除结果 = %v，期望 %v", got, c.wantDel)
			}
			if len(st.calls) != c.wantCalls {
				t.Fatalf("UninstallService 调用 %d 次，期望 %d 次（calls=%v）", len(st.calls), c.wantCalls, st.calls)
			}
		})
	}
}
