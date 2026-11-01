package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ntpsync/internal/client"
	"ntpsync/internal/config"
	"ntpsync/internal/pal"
)

// TestNewUpstreamClientNoStepGuard 锁住「服务端向上游校时不设阈值」这一行为。
//
// 该断言只看配置，不发起任何网络请求，也绝不改动系统时钟——真正「偏差 17 秒
// 仍会设钟」的行为由 internal/client 用假平台验证，此处只保证接线没被回退。
func TestNewUpstreamClientNoStepGuard(t *testing.T) {
	c, err := newUpstreamClient("127.0.0.1:12399")
	if err != nil {
		t.Fatalf("newUpstreamClient 失败: %v", err)
	}
	if c.MaxStep != client.MaxStepUnlimited {
		t.Fatalf("上游客户端应不设跳变阈值（MaxStep=%v），实际 %v（默认护栏 %v 会挡掉大偏差）",
			client.MaxStepUnlimited, c.MaxStep, client.DefaultMaxStep)
	}
	if c.MaxStep >= 0 {
		t.Fatalf("MaxStep 必须为负值表示不设阈值，实际 %v", c.MaxStep)
	}
	// 确保没顺手改掉普通客户端默认值：客户端校时仍受 5 秒护栏保护。
	plain, err := client.New("127.0.0.1:12399")
	if err != nil {
		t.Fatalf("client.New 失败: %v", err)
	}
	if plain.MaxStep != client.DefaultMaxStep {
		t.Fatalf("普通客户端默认阈值应为 %v，实际 %v", client.DefaultMaxStep, plain.MaxStep)
	}
	if plain.MaxStep != 5*time.Second {
		t.Fatalf("默认阈值应为 5 秒，实际 %v", plain.MaxStep)
	}
}

// TestNewUpstreamClientNormalizesAddr 确认上游地址仍会自动补 :123 端口。
func TestNewUpstreamClientNormalizesAddr(t *testing.T) {
	c, err := newUpstreamClient("ntp.aliyun.com")
	if err != nil {
		t.Fatalf("newUpstreamClient 失败: %v", err)
	}
	if c.ServerAddr != "ntp.aliyun.com:123" {
		t.Fatalf("应补默认端口，实际 %q", c.ServerAddr)
	}
}

// TestUpstreamResultText 锁住展示文案：不能再出现「已同步 + 未设钟」并存的自相矛盾表述。
func TestUpstreamResultText(t *testing.T) {
	cases := []struct {
		name     string
		res      client.Result
		wantText string
	}{
		{
			name:     "已设钟",
			res:      client.Result{Offset: 17 * time.Second, Applied: true},
			wantText: "已设钟（调整 17s）",
		},
		{
			name:     "因偏差极小未设钟",
			res:      client.Result{Skipped: true, Reason: "偏差 <1ms，无需修改"},
			wantText: "未设钟：偏差 <1ms，无需修改",
		},
		{
			name:     "因未提权未设钟",
			res:      client.Result{Skipped: true, Reason: "修改系统时钟需要管理员(Windows)/root(Linux)权限"},
			wantText: "未设钟：修改系统时钟需要管理员(Windows)/root(Linux)权限",
		},
	}
	for _, c := range cases {
		synced, text := upstreamResultText(&c.res)
		if !synced {
			t.Errorf("%s：通信成功时 synced 应为 true", c.name)
		}
		if text != c.wantText {
			t.Errorf("%s：文案应为 %q，实际 %q", c.name, c.wantText, text)
		}
		// 关键回归点：未设钟时不得出现「已同步」字样。
		if c.res.Skipped && strings.Contains(text, "已同步") {
			t.Errorf("%s：未设钟文案不应包含「已同步」，实际 %q", c.name, text)
		}
	}
}

// TestPlanServerInstall_KeepExisting 覆盖「安装器要求保留旧配置」的全部分支。
// 升级时若误判，用户在 Web 里配的监听地址/上游会被一次重装抹掉。
func TestPlanServerInstall_KeepExisting(t *testing.T) {
	old := &config.ServerConfig{
		Listen: "192.168.55.10:1123", Stratum: 3, Web: "127.0.0.1:8080",
		Upstream: "ntp.aliyun.com", UpstreamInterval: "2小时",
	}
	cases := []struct {
		name       string
		preserve   bool
		exists     bool
		old        *config.ServerConfig
		loadErr    error
		wantKeep   bool
		wantListen string
	}{
		{"要求保留且配置可读 → 保留", true, true, old, nil, true, "192.168.55.10:1123"},
		{"没要求保留 → 覆盖", false, true, old, nil, false, "0.0.0.0:123"},
		{"要求保留但文件不存在 → 覆盖", true, false, nil, nil, false, "0.0.0.0:123"},
		{"要求保留但配置损坏 → 覆盖", true, true, nil, errors.New("bad json"), false, "0.0.0.0:123"},
	}
	for _, c := range cases {
		got := planServerInstall("0.0.0.0:123", 2, "127.0.0.1:8080", "", time.Hour,
			c.preserve, c.exists, c.old, c.loadErr)
		if got.Keep != c.wantKeep {
			t.Errorf("%s：Keep 期望 %v，实际 %v", c.name, c.wantKeep, got.Keep)
		}
		if got.Listen != c.wantListen {
			t.Errorf("%s：监听地址期望 %q，实际 %q（防火墙按它放行，错了会放行错端口）",
				c.name, c.wantListen, got.Listen)
		}
	}
}

// TestPlanServerInstall_EmptyOldListen 旧配置没写监听时回落到本次参数，
// 不能让防火墙去放行空端口。
func TestPlanServerInstall_EmptyOldListen(t *testing.T) {
	got := planServerInstall("0.0.0.0:123", 2, "127.0.0.1:8080", "", time.Hour,
		true, true, &config.ServerConfig{Upstream: "ntp.aliyun.com"}, nil)
	if !got.Keep {
		t.Fatal("旧配置可读时应保留")
	}
	if got.Listen != "0.0.0.0:123" {
		t.Errorf("旧配置无监听地址时应回落本次参数，实际 %q", got.Listen)
	}
}

// TestPlanServerInstall_FillMissingWeb 覆盖升级且旧配置没有 web 字段：
// 必须把本次的 -web 补写进去，否则升级后 Web 界面不启动；已配过的端口则不能动。
func TestPlanServerInstall_FillMissingWeb(t *testing.T) {
	old := &config.ServerConfig{Listen: "0.0.0.0:123", Stratum: 2, Upstream: "ntp.aliyun.com"}
	got := planServerInstall("0.0.0.0:123", 2, "0.0.0.0:8080", "", time.Hour, true, true, old, nil)
	if !got.Keep {
		t.Fatal("旧配置可读时应保留业务参数")
	}
	if !got.Write {
		t.Fatal("旧配置缺 web 时必须补写，否则服务重启后 Web 界面不启动")
	}
	if got.Saved == nil || got.Saved.Web != "0.0.0.0:8080" {
		t.Fatalf("补写应写入本次的 web，实际 %+v", got.Saved)
	}
	if got.Saved.Listen != "0.0.0.0:123" || got.Saved.Upstream != "ntp.aliyun.com" {
		t.Errorf("补写只该动 web、其余保持原值，实际 %+v", *got.Saved)
	}

	// 旧配置已有 web：保留旧端口、不写盘；防火墙也应按这个生效值取端口。
	old2 := &config.ServerConfig{Listen: "0.0.0.0:123", Web: "0.0.0.0:9999"}
	got2 := planServerInstall("0.0.0.0:123", 2, "0.0.0.0:8080", "", time.Hour, true, true, old2, nil)
	if got2.Write {
		t.Error("已有 web 时不该落盘，否则会把用户改过的端口顶回去")
	}
	if got2.Saved == nil || got2.Saved.Web != "0.0.0.0:9999" {
		t.Errorf("生效值应为旧配置的端口（防火墙按它放行），实际 %+v", got2.Saved)
	}

	// 全新安装：写完整配置。
	if p := planServerInstall("0.0.0.0:123", 2, "0.0.0.0:8080", "", time.Hour, true, false, nil, nil); !p.Write || p.Saved == nil || p.Saved.Web != "0.0.0.0:8080" {
		t.Errorf("全新安装应写入完整配置，实际 keep=%v write=%v saved=%+v", p.Keep, p.Write, p.Saved)
	}
}

// fakeServicePlatform 记录卸载时传入的服务名与撤销的防火墙端口
// （卸载需提权，故只测决策与调用参数）。
type fakeServicePlatform struct {
	pal.Platform
	uninstalled []string
	udpPorts    []int
	tcpPorts    []int
	err         error
}

func (f *fakeServicePlatform) UninstallService(name string) error {
	f.uninstalled = append(f.uninstalled, name)
	return f.err
}

func (f *fakeServicePlatform) RemoveFirewall(port int) error {
	f.udpPorts = append(f.udpPorts, port)
	return nil
}

func (f *fakeServicePlatform) RemoveFirewallTCP(port int) error {
	f.tcpPorts = append(f.tcpPorts, port)
	return nil
}

// TestUninstallServerKeepsConfigWhenServiceRemovalFails 服务没卸掉时不能删配置。
//
// 卸载要提权。非管理员跑 uninstall 时如果照样把配置删了，产品却还在，
// 它下次启动就会退回默认参数（123/8080），现场表现为「配置莫名其妙被重置」。
// 所以这里的正确行为是：报错退出，配置与统计原样保留，防火墙也不动。
func TestUninstallServerKeepsConfigWhenServiceRemovalFails(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ntpsyncd.json")
	statsPath := filepath.Join(dir, config.ServerStatsFileName)
	if err := config.Save(cfgPath, &config.ServerConfig{Listen: "0.0.0.0:1123"}); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	if err := os.WriteFile(statsPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("写统计失败: %v", err)
	}

	fp := &fakeServicePlatform{err: errors.New("拒绝访问")}
	if err := uninstallServer(fp, cfgPath, false); err == nil {
		t.Fatal("服务卸载失败时必须返回错误")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("服务未卸载，配置应保留: %v", err)
	}
	if _, err := os.Stat(statsPath); err != nil {
		t.Errorf("服务未卸载，授时统计应保留: %v", err)
	}
	if len(fp.udpPorts)+len(fp.tcpPorts) != 0 {
		t.Errorf("服务未卸载时不该撤防火墙: udp=%v tcp=%v", fp.udpPorts, fp.tcpPorts)
	}
}

// TestRemoveServerServiceUsesServerName 锁住服务端只卸载自己的 SntpServer。
func TestRemoveServerServiceUsesServerName(t *testing.T) {
	fp := &fakeServicePlatform{}
	removed, err := removeServerService(fp)
	if err != nil {
		t.Fatalf("卸载返回错误: %v", err)
	}
	if !removed {
		t.Fatal("真正删掉服务时 removed 应为 true")
	}
	if len(fp.uninstalled) != 1 || fp.uninstalled[0] != pal.DefaultServiceName {
		t.Fatalf("服务端卸载应传 %q，实际 %v", pal.DefaultServiceName, fp.uninstalled)
	}
}

// TestRemoveServerServiceNotInstalled 验证「本来就没装」不算失败。
func TestRemoveServerServiceNotInstalled(t *testing.T) {
	fp := &fakeServicePlatform{
		err: fmt.Errorf("%w: %s", pal.ErrServiceNotInstalled, pal.DefaultServiceName),
	}
	removed, err := removeServerService(fp)
	if err != nil {
		t.Fatalf("服务未安装不应返回错误: %v", err)
	}
	if removed {
		t.Fatal("服务未安装时 removed 应为 false")
	}
}

// TestUninstallServerCleansFirewallAndData 锁住卸载的实际清理范围：
// 只卸自己的服务、防火墙按配置里的端口（外加历史默认端口兜底）、
// 配置与授时统计（连 .tmp）全删，数据目录空了顺带删掉。
//
// 这些断言对应现场最容易怀疑「没卸干净」的三处：服务名、端口放行、%ProgramData% 里的文件。
func TestUninstallServerCleansFirewallAndData(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ntpsyncd.json")
	statsPath := clientStatsPath(cfgPath)

	// 先写配置，再补 .tmp：saveJSON 自己会用一次 .tmp 并改名，顺序反了就测不到它
	if err := config.Save(cfgPath, &config.ServerConfig{Listen: "0.0.0.0:1123", Web: "0.0.0.0:8180"}); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	for _, p := range []string{cfgPath + ".tmp", statsPath, statsPath + ".tmp"} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatalf("准备 %s 失败: %v", p, err)
		}
	}

	fp := &fakeServicePlatform{}
	if err := uninstallServer(fp, cfgPath, false); err != nil {
		t.Fatalf("卸载返回错误: %v", err)
	}
	if len(fp.uninstalled) != 1 || fp.uninstalled[0] != pal.DefaultServiceName {
		t.Errorf("只应卸载服务端自己的服务 %q，实际 %v", pal.DefaultServiceName, fp.uninstalled)
	}
	// 1123 = 配置里的监听端口；123 = 历史默认（老版本按它建过规则）
	if want := []int{1123, 123}; !reflect.DeepEqual(fp.udpPorts, want) {
		t.Errorf("撤销的 UDP 放行 = %v，期望 %v", fp.udpPorts, want)
	}
	// 8180 = 配置里的 Web 端口；8080 = 默认；123 = 1.11.2 那版「Web 端口跟随授时端口」
	if want := []int{8180, 8080, 123}; !reflect.DeepEqual(fp.tcpPorts, want) {
		t.Errorf("撤销的 TCP 放行 = %v，期望 %v", fp.tcpPorts, want)
	}
	for _, p := range []string{cfgPath, cfgPath + ".tmp", statsPath, statsPath + ".tmp"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应已删除", p)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("数据目录已空，应一并删除")
	}
}

// TestUninstallServerKeepConfig -keep-config 只撤服务与防火墙，配置文件留着。
func TestUninstallServerKeepConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ntpsyncd.json")
	if err := config.Save(cfgPath, &config.ServerConfig{Listen: "0.0.0.0:123"}); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	if err := uninstallServer(&fakeServicePlatform{}, cfgPath, true); err != nil {
		t.Fatalf("卸载返回错误: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("-keep-config 时配置应保留: %v", err)
	}
}
