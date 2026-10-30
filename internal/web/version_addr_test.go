package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ntpsync/internal/server"
)

// TestNormalizeListenAddr 覆盖 Web 地址的几种写法。
// 「只写端口」这一条是现场真实踩过的：静默参数 /WEB=8080 与手工运行 -web 8080
// 都会写成纯端口，直接交给 net.Listen 会报 missing port。
func TestNormalizeListenAddr(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "", want: ""},
		{in: "8080", want: "127.0.0.1:8080"},
		{in: " 8090 ", want: "127.0.0.1:8090"},
		{in: "127.0.0.1:8080", want: "127.0.0.1:8080"},
		{in: "0.0.0.0:8080", want: "0.0.0.0:8080"},
		{in: "[::1]:8080", want: "[::1]:8080"},
		{in: "  ", want: ""},
	}
	for _, c := range cases {
		if got := NormalizeListenAddr(c.in); got != c.want {
			t.Errorf("NormalizeListenAddr(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestDisplayVersion 未注入版本时不能把 "dev" 这种内部字样丢给用户。
func TestDisplayVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "", want: "开发版"},
		{in: "dev", want: "开发版"},
		{in: "1.10.28", want: "v1.10.28"},
		{in: "v1.10.28", want: "v1.10.28"},
	}
	for _, c := range cases {
		if got := displayVersion(c.in); got != c.want {
			t.Errorf("displayVersion(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestAPIClientsCSV 校验导出文件的三个细节：附件头、UTF-8 BOM、中文表头。
// BOM 与 CRLF 决定了 Excel 打开后是不是一屏乱码、有没有正确分行。
func TestAPIClientsCSV(t *testing.T) {
	// 客户端模式（Server 为 nil）没有用户列表可导，这里用真实服务端实例。
	h := New(server.New(server.Config{Listen: "127.0.0.1:0"}), DefaultConfig())
	h.Version = "1.10.28"
	mux := http.NewServeMux()
	h.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/clients?format=csv", nil))

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Errorf("Content-Type = %q，期望 text/csv", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") ||
		!strings.Contains(cd, ".csv") {
		t.Errorf("Content-Disposition = %q，期望附件且文件名以 .csv 结尾", cd)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Error("缺少 UTF-8 BOM，Excel 打开会乱码")
	}
	if !strings.Contains(body, "客户端 IP,授时次数,首次授时,最近授时") {
		t.Errorf("表头缺失或不符：%q", body)
	}
	if !strings.Contains(body, "版本=v1.10.28") {
		t.Errorf("导出内容应带版本号：%q", body)
	}
	if !strings.Contains(body, "\r\n") {
		t.Error("行尾应为 CRLF，Excel 才能正确分行")
	}
}

// TestAPILogsFormats 同一个接口两种形态：默认 JSON 给界面用，format=txt 给下载用。
func TestAPILogsFormats(t *testing.T) {
	h := New(nil, DefaultConfig())
	h.Logf("第一条日志")
	h.Logf("第二条日志")
	mux := http.NewServeMux()
	h.Routes(mux)

	// 默认 JSON
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs", nil))
	if !strings.Contains(rec.Body.String(), "第一条日志") {
		t.Errorf("JSON 形态应包含日志内容：%q", rec.Body.String())
	}
	if rec.Header().Get("Content-Disposition") != "" {
		t.Error("JSON 形态不应带附件头")
	}

	// 纯文本下载
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs?format=txt", nil))
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") ||
		!strings.Contains(cd, ".txt") {
		t.Errorf("下载形态 Content-Disposition 不正确：%q", cd)
	}
	if strings.Contains(rec.Body.String(), "{") {
		t.Error("下载形态不应输出 JSON")
	}
	if !strings.Contains(rec.Body.String(), "第一条日志") {
		t.Errorf("下载内容缺失：%q", rec.Body.String())
	}
}

// TestAPIStatusIncludesVersion 状态接口必须带版本，界面靠它显示。
func TestAPIStatusIncludesVersion(t *testing.T) {
	h := New(nil, DefaultConfig())
	h.Version = "1.10.28"
	mux := http.NewServeMux()
	h.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if !strings.Contains(rec.Body.String(), `"version":"v1.10.28"`) {
		t.Errorf("状态接口缺少版本字段：%q", rec.Body.String())
	}
}
