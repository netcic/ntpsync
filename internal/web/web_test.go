package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ntpsync/internal/server"
)

// newTestHandler 构造一个最小可用的 Handler（服务端实例不启动，仅承载状态）。
// 传 nil 表示纯客户端模式。
func newTestHandler(t *testing.T, s *server.Server, cfg *Config) *Handler {
	t.Helper()
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return New(s, cfg)
}

// postUpstreamSync 调用手动上游同步接口，返回状态码与响应体。
func postUpstreamSync(t *testing.T, h *Handler) (int, map[string]interface{}) {
	t.Helper()
	mux := http.NewServeMux()
	h.Routes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/server/upstream/sync", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body map[string]interface{}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	return rec.Code, body
}

// TestUpstreamSyncClientMode 客户端模式（Server 为 nil）应拒绝并给出明确提示。
func TestUpstreamSyncClientMode(t *testing.T) {
	h := newTestHandler(t, nil, nil)
	code, body := postUpstreamSync(t, h)
	if code != http.StatusBadRequest {
		t.Fatalf("客户端模式应返回 400，实际 %d", code)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "客户端模式") {
		t.Errorf("错误提示应说明客户端模式，实际 %q", msg)
	}
}

// TestUpstreamSyncNotConfigured 服务端模式但未配置上游时，应提示先配置。
func TestUpstreamSyncNotConfigured(t *testing.T) {
	s := server.New(server.Config{Listen: "0.0.0.0:0"})
	cfg := DefaultConfig()
	cfg.Upstream = "" // 显式清空
	h := newTestHandler(t, s, cfg)
	h.SetUpstreamSync(func(string) { t.Fatal("未配置上游时不应执行同步") })

	code, body := postUpstreamSync(t, h)
	if code != http.StatusBadRequest {
		t.Fatalf("未配置上游应返回 400，实际 %d", code)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "未配置上游时间源") {
		t.Errorf("错误提示应说明未配置上游，实际 %q", msg)
	}
}

// TestUpstreamSyncNotInjected 未注入同步函数时应返回 503，而不是空指针崩溃。
func TestUpstreamSyncNotInjected(t *testing.T) {
	s := server.New(server.Config{Listen: "0.0.0.0:0"})
	cfg := DefaultConfig()
	cfg.Upstream = "ntp.example.lan"
	h := newTestHandler(t, s, cfg)

	code, body := postUpstreamSync(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未注入同步函数应返回 503，实际 %d", code)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Error("应返回错误提示")
	}
}

// TestUpstreamSyncSuccess 正常路径：注入的函数收到界面配置的上游地址，
// 并把结果写回服务端状态，接口应返回该结果的完整字段。
func TestUpstreamSyncSuccess(t *testing.T) {
	s := server.New(server.Config{Listen: "0.0.0.0:0"})
	cfg := DefaultConfig()
	cfg.Upstream = "ntp.example.lan" // 注意：地址带端口省略，由 client 层补

	h := newTestHandler(t, s, cfg)

	var gotAddr string
	h.SetUpstreamSync(func(addr string) {
		gotAddr = addr
		// 模拟真实同步：把结果写回服务端状态（这是接口读取的唯一来源）。
		s.SetUpstreamStatus(&server.UpstreamStatus{
			Addr:   addr,
			Offset: "12ms",
			Delay:  "3ms",
			Result: "已同步（调整 12ms）",
			Synced: true,
			LastAt: time.Now(),
		})
	})

	code, body := postUpstreamSync(t, h)
	if code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d，body=%v", code, body)
	}
	// 注入函数必须收到界面上配置的地址（而非启动参数里的旧值）。
	if gotAddr != "ntp.example.lan" {
		t.Errorf("同步地址应为界面配置值，实际 %q", gotAddr)
	}
	if synced, _ := body["synced"].(bool); !synced {
		t.Error("响应 synced 应为 true")
	}
	if got, _ := body["offset"].(string); got != "12ms" {
		t.Errorf("offset 应为 12ms，实际 %q", got)
	}
	if got, _ := body["result"].(string); !strings.Contains(got, "已同步") {
		t.Errorf("result 应含「已同步」，实际 %q", got)
	}
	if got, _ := body["last_at"].(string); got == "" {
		t.Error("last_at 不应为空")
	}
}

// TestUpstreamSyncPrefersConfigAddr 验证地址优先级：
// 界面配置值优先于启动参数里的上游（改完配置无需重启即可验证）。
func TestUpstreamSyncPrefersConfigAddr(t *testing.T) {
	// 启动参数里的上游为 old.lan。
	s := server.New(server.Config{Listen: "0.0.0.0:0", Upstream: "old.lan"})
	cfg := DefaultConfig()
	cfg.Upstream = "new.lan" // 界面改成新地址

	h := newTestHandler(t, s, cfg)
	var gotAddr string
	h.SetUpstreamSync(func(addr string) {
		gotAddr = addr
		s.SetUpstreamStatus(&server.UpstreamStatus{Addr: addr, Synced: true, LastAt: time.Now(), Result: "ok"})
	})

	if code, body := postUpstreamSync(t, h); code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d，body=%v", code, body)
	}
	if gotAddr != "new.lan" {
		t.Errorf("应优先使用界面配置地址 new.lan，实际 %q", gotAddr)
	}
}

// TestUpstreamSyncMethodNotAllowed GET 应被拒绝。
func TestUpstreamSyncMethodNotAllowed(t *testing.T) {
	s := server.New(server.Config{Listen: "0.0.0.0:0"})
	h := newTestHandler(t, s, DefaultConfig())
	mux := http.NewServeMux()
	h.Routes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/server/upstream/sync", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应返回 405，实际 %d", rec.Code)
	}
}

// TestDefaultConfigChineseInterval 默认上游间隔必须是中文描述，不能是 "1h0m0s"。
func TestDefaultConfigChineseInterval(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.UpstreamInterval != "1小时" {
		t.Errorf("默认上游间隔应为「1小时」，实际 %q", cfg.UpstreamInterval)
	}
}

// TestConfigSavesChineseInterval 保存时把机器写法归一化为中文回显。
func TestConfigSavesChineseInterval(t *testing.T) {
	cases := []struct{ in, want string }{
		{"90m", "1小时30分钟"},
		{"30分", "30分钟"},
		{"2小时", "2小时"},
		{"45s", "45秒"},
	}
	for _, c := range cases {
		h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), nil)
		mux := http.NewServeMux()
		h.Routes(mux)
		body := strings.NewReader(`{"upstream_interval":"` + c.in + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/config", body)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存 %q 失败，状态码 %d", c.in, rec.Code)
		}
		if h.Config.UpstreamInterval != c.want {
			t.Errorf("输入 %q 应回显 %q，实际 %q", c.in, c.want, h.Config.UpstreamInterval)
		}
	}
}

// TestStaticFavicon 内嵌 favicon.ico 必须可被浏览器取到，且是合法的 ICO。
// 图标从 embedding 里掉出去时（改 embed 指令、换目录）这里会立刻报警。
func TestStaticFavicon(t *testing.T) {
	mux := http.NewServeMux()
	newTestHandler(t, nil, nil).Routes(mux)
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /favicon.ico 应返回 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "image/x-icon") {
		t.Errorf("Content-Type 应为 image/x-icon，实际 %q", ct)
	}
	body := rec.Body.Bytes()
	// ICO 头：reserved=0(2B) + type=1(2B) + 图像数量(2B)
	if len(body) < 6 || body[0] != 0 || body[1] != 0 || body[2] != 1 || body[3] != 0 {
		t.Fatalf("返回内容不是合法 ICO（前 6 字节需为 00 00 01 00 NN 00）")
	}
	if n := int(body[4]) | int(body[5])<<8; n < 2 {
		t.Errorf("ICO 应含多个尺寸以便浏览器按 DPR 选择，实际 %d 个", n)
	}
}

// TestIndexBrandAndIcon 页面必须带品牌图标与「Sntpserver」标识。
// 早前 base64 图标注入曾把整个 HTML 写坏（缺 style/body），这里做结构性兜底。
func TestIndexBrandAndIcon(t *testing.T) {
	mux := http.NewServeMux()
	newTestHandler(t, nil, nil).Routes(mux)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / 应返回 200，实际 %d", rec.Code)
	}
	html := rec.Body.String()

	wants := []struct{ name, sub string }{
		{"品牌图标", `id="brandIco"`},
		{"图标为内嵌图片", `class="logo-ico"`},
		{"默认品牌名", `Sntpserver`},
		{"favicon 指向 ICO", `href="/favicon.ico"`},
		{"样式块", `<style>`},
		{"状态详情容器", `id="statusDetail"`},
	}
	for _, w := range wants {
		if !strings.Contains(html, w.sub) {
			t.Errorf("页面缺少%s（期望包含 %q）", w.name, w.sub)
		}
	}
	// 结构完整性：起止标签必须成对出现，避免再次出现「只有半页」的事故。
	for _, tag := range []string{"<style>", "</style>", "<body>", "</body>", "</html>"} {
		if strings.Count(html, tag) != 1 {
			t.Errorf("标签 %q 出现 %d 次，应为 1 次", tag, strings.Count(html, tag))
		}
	}
	// 已废弃的旧节点不应回潮（否则 JS 会空指针）。
	for _, dead := range []string{`id="serverKv"`, `id="kvTitle"`, `class="addr-block"`, `class="addr-v"`} {
		if strings.Contains(html, dead) {
			t.Errorf("页面仍残留已移除的节点 %q", dead)
		}
	}
	// 监听地址用大字号 KV 值呈现；一旦退回独立块，左右两卡的行节奏会再次错位。
	if !strings.Contains(html, `class="v-big"`) || !strings.Contains(html, `min-height:46px`) {
		t.Error("状态总览详情行缺少统一行高/大字号值样式，两卡将无法逐行对齐")
	}
}
