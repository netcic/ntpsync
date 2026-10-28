package main

import (
	"errors"
	"fmt"
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

// fakeServicePlatform 记录卸载时传入的服务名（卸载需提权，故只测决策部分）。
type fakeServicePlatform struct {
	pal.Platform
	uninstalled []string
	err         error
}

func (f *fakeServicePlatform) UninstallService(name string) error {
	f.uninstalled = append(f.uninstalled, name)
	return f.err
}

// TestRemoveServerServiceUsesServerName 锁住服务端只卸载自己的 Sntpserver。
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
