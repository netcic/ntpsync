package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ntpsync/internal/server"
)

// lastLog 取日志缓冲里的最后一条（Logf 同时写缓冲与日志文件，内容一致）。
func lastLog(h *Handler) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.logBuf) == 0 {
		return ""
	}
	return h.logBuf[len(h.logBuf)-1]
}

// TestConfigSummaryServerMode 服务端模式只记服务端真正用到的字段，
// 不得出现客户端专属项留下的 `server=` / `interval=` / `step=` 空值。
func TestConfigSummaryServerMode(t *testing.T) {
	cfg := &Config{
		Listen:           "0.0.0.0:123",
		Stratum:          2,
		Upstream:         "ntp1.aliyun.com",
		UpstreamInterval: "1小时",
	}
	h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), cfg)
	got := h.configSummary(cfg)

	for _, want := range []string{"监听=0.0.0.0:123", "层级=2", "上游=ntp1.aliyun.com", "上游间隔=1小时"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q，实际 %q", want, got)
		}
	}
	// 空值项一律不出现（这是本次修复的核心）。
	for _, dead := range []string{"server=", "interval=", "step="} {
		if strings.Contains(got, dead) {
			t.Errorf("摘要不应包含空字段 %q，实际 %q", dead, got)
		}
	}
}

// TestConfigSummaryServerModeNoUpstream 上游留空是合法配置，
// 应显式写成「本机硬件时钟」，不能直接少一项让人以为没保存上。
func TestConfigSummaryServerModeNoUpstream(t *testing.T) {
	cfg := &Config{Listen: "0.0.0.0:123", Stratum: 2}
	h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), cfg)
	got := h.configSummary(cfg)

	if !strings.Contains(got, "上游=本机硬件时钟") {
		t.Errorf("未配上游时应写明本机硬件时钟，实际 %q", got)
	}
	if strings.Contains(got, "上游间隔=") {
		t.Errorf("未配上游时不应打印上游间隔，实际 %q", got)
	}
}

// TestConfigSummaryClientMode 客户端模式只记校时相关字段。
func TestConfigSummaryClientMode(t *testing.T) {
	cfg := &Config{ServerAddr: "192.168.55.105:123", Interval: "15分钟", Step: "5秒"}
	h := newTestHandler(t, nil, cfg)
	got := h.configSummary(cfg)

	for _, want := range []string{"校时目标=192.168.55.105:123", "校时间隔=15分钟", "校时阈值=5秒"} {
		if !strings.Contains(got, want) {
			t.Errorf("摘要缺少 %q，实际 %q", want, got)
		}
	}
	for _, dead := range []string{"监听=", "层级=", "上游="} {
		if strings.Contains(got, dead) {
			t.Errorf("客户端摘要不应包含服务端字段 %q，实际 %q", dead, got)
		}
	}
}

// TestConfigSummaryEmpty 全空时也要有一句可读的话，不能只剩下前缀。
func TestConfigSummaryEmpty(t *testing.T) {
	h := newTestHandler(t, nil, &Config{})
	if got := h.configSummary(&Config{}); got != "无变更字段" {
		t.Errorf("全空时应返回「无变更字段」，实际 %q", got)
	}
}

// TestConfigSaveLogHasNoEmptyFields 端到端校验保存配置写进日志的那一行：
// 服务端模式的日志里不能出现空字段占位。
func TestConfigSaveLogHasNoEmptyFields(t *testing.T) {
	h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), DefaultConfig())
	mux := http.NewServeMux()
	h.Routes(mux)

	body := strings.NewReader(`{"listen":"0.0.0.0:123","stratum":2,"upstream":"ntp1.aliyun.com","upstream_interval":"1h"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/config", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存配置失败，状态码 %d", rec.Code)
	}

	line := lastLog(h)
	if !strings.HasPrefix(line, "[") || !strings.Contains(line, "配置已更新：") {
		t.Fatalf("最后一条日志应为配置已更新，实际 %q", line)
	}
	if !strings.Contains(line, "上游=ntp1.aliyun.com") {
		t.Errorf("日志应包含上游地址，实际 %q", line)
	}
	if strings.Contains(line, "= ") || strings.HasSuffix(line, "=") {
		t.Errorf("日志不应出现空值字段，实际 %q", line)
	}
}
