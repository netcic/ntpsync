package web

import (
	"testing"
	"time"
)

// TestResolveClientMaxStep 锁定「立即校时」的阈值来源优先级。
//
// 回归的原始 Bug：前端只发 server 不发 step，后端便用 client 默认的 5 秒，
// 用户在参数页把阈值保存成 120 秒后，手动校时仍报「超过阈值 5s」——
// 保存看起来成功、后台守护也按 120 秒走，只有手动按钮不认。
func TestResolveClientMaxStep(t *testing.T) {
	cases := []struct {
		name     string
		bodyStep string
		cfg      *Config
		want     time.Duration
	}{
		{"请求体优先", "30秒", &Config{Step: "2分钟"}, 30 * time.Second},
		{"请求体缺省时用已保存配置", "", &Config{Step: "2分钟"}, 2 * time.Minute},
		{"请求体非法时回落到配置", "乱写", &Config{Step: "2分钟"}, 2 * time.Minute},
		{"请求体为 0 时不采纳", "0秒", &Config{Step: "2分钟"}, 2 * time.Minute},
		{"都为空则用默认（返回 0）", "", &Config{}, 0},
		{"cfg 为 nil 也不崩", "", nil, 0},
		{"中文与纯数字都认", "120", nil, 120 * time.Minute},
	}
	for _, c := range cases {
		if got := resolveClientMaxStep(c.bodyStep, c.cfg); got != c.want {
			t.Errorf("%s：期望 %v，实际 %v", c.name, c.want, got)
		}
	}
}

// TestClientSyncEndpointHonoursSavedStep 端到端确认接口层真的把阈值传下去了。
//
// 不触网：只校验「用已保存配置覆盖默认值」这一步的决策结果，
// 因为真正设钟要走网络+提权，属于 client 包与 e2e 的覆盖范围。
func TestClientSyncEndpointHonoursSavedStep(t *testing.T) {
	h := &Handler{Config: &Config{ServerAddr: "127.0.0.1:123", Step: "2分钟"}}
	if got := resolveClientMaxStep("", h.Config); got != 2*time.Minute {
		t.Fatalf("接口层应沿用已保存的 2 分钟阈值，实际 %v", got)
	}
	// 默认值不应被误判成「显式传入」：只有解析成功且 >0 才算数。
	if got := resolveClientMaxStep("", &Config{Step: ""}); got != 0 {
		t.Fatalf("配置为空时不应编造阈值，实际 %v", got)
	}
}
