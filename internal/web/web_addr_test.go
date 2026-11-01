package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ntpsync/internal/server"
)

// TestValidateWebAddr 校验 Web 监听地址的闸门。
//
// 这个值会被写进配置文件、下次启动直接拿去 ListenAndServe。放过一个非法端口，
// 重启后 Web 起不来、界面也连不上，用户只能手改配置文件自救，所以这里必须严格。
func TestValidateWebAddr(t *testing.T) {
	good := []string{"127.0.0.1:8080", "0.0.0.0:80", "192.168.1.10:65535", "8080"}
	for _, a := range good {
		if err := ValidateWebAddr(a); err != nil {
			t.Errorf("%q 应合法，却报错：%v", a, err)
		}
	}
	bad := []string{"", "   ", "0.0.0.0", "0.0.0.0:abc", "0.0.0.0:0", "0.0.0.0:65536", "0.0.0.0:-1"}
	for _, a := range bad {
		if err := ValidateWebAddr(a); err == nil {
			t.Errorf("%q 应被拒绝，却通过了", a)
		}
	}
}

// TestValidateListenAddr 授时监听地址同样不许写坏：端口非法时服务端起不来，
// 现场表现是「客户端全部校不到时」，比 Web 打不开更难查。
func TestValidateListenAddr(t *testing.T) {
	good := []string{"0.0.0.0:123", "127.0.0.1:123", "192.168.1.10:1123", "[::]:123", "123"}
	for _, a := range good {
		if err := ValidateListenAddr(a); err != nil {
			t.Errorf("%q 应合法，却报错：%v", a, err)
		}
	}
	bad := []string{"", "0.0.0.0", "0.0.0.0:0", "0.0.0.0:70000", "0.0.0.0:abc", "2001:db8::1:123"}
	for _, a := range bad {
		if err := ValidateListenAddr(a); err == nil {
			t.Errorf("%q 应被拒绝，却通过了", a)
		}
	}
}

// TestAPIConfigWebPortIndependent 是本次改动的核心断言：
// 配置接口改 Web 端口只改端口、主机固定 0.0.0.0；改授时监听端口不能把 Web 端口带走。
// （界面自 1.11.4 起不再提交 web_addr，接口仍保留兼容，故断言仍在接口层。）
func TestAPIConfigWebPortIndependent(t *testing.T) {
	h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), &Config{Listen: "0.0.0.0:123", Stratum: 2})
	h.WebAddr = "0.0.0.0:8080"

	// GET：Config 里没填时应回落到实际监听地址。
	if got := doConfig(t, h, http.MethodGet, ""); got.WebAddr != "0.0.0.0:8080" {
		t.Fatalf("GET 应回显当前监听地址，实际 %q", got.WebAddr)
	}

	var saved *Config
	h.OnSave = func(cfg *Config) error {
		cp := *cfg
		saved = &cp
		return nil
	}

	// POST：改 Web 端口 → 端口变、主机仍是 0.0.0.0（即使前端提交的是回环地址）。
	got := doConfig(t, h, http.MethodPost, `{"stratum":2,"web_addr":"127.0.0.1:18080"}`)
	if got.WebAddr != "0.0.0.0:18080" {
		t.Fatalf("POST 后应为 0.0.0.0:18080，实际 %q", got.WebAddr)
	}
	if saved == nil || saved.WebAddr != "0.0.0.0:18080" {
		t.Fatalf("OnSave 应收到 0.0.0.0:18080，实际 %+v", saved)
	}

	// POST：只改授时监听地址 → Web 端口必须原地不动（不跟监听端口联动）。
	got = doConfig(t, h, http.MethodPost, `{"stratum":2,"listen":"0.0.0.0:1123"}`)
	if got.WebAddr != "0.0.0.0:18080" {
		t.Errorf("改监听地址不该动 Web 端口，实际 %q", got.WebAddr)
	}
	if got.Listen != "0.0.0.0:1123" {
		t.Errorf("监听地址应为 0.0.0.0:1123，实际 %q", got.Listen)
	}
}

// TestAPIConfigRejectsBadAddr 端口非法时必须 400 且不触发落盘、不改内存，
// 否则配置文件里会留下一个下次启动会炸的值。
func TestAPIConfigRejectsBadAddr(t *testing.T) {
	cases := []struct{ name, body string }{
		{"监听端口越界", `{"stratum":2,"listen":"0.0.0.0:70000"}`},
		{"Web 端口非数字", `{"stratum":2,"web_addr":"0.0.0.0:abc"}`},
		{"Web 端口越界", `{"stratum":2,"web_addr":"0.0.0.0:65536"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHandler(t, server.New(server.Config{Listen: "0.0.0.0:0"}), &Config{Listen: "0.0.0.0:123", Stratum: 2})
			h.WebAddr = "0.0.0.0:8080"
			called := false
			h.OnSave = func(cfg *Config) error {
				called = true
				return nil
			}

			mux := http.NewServeMux()
			h.Routes(mux)
			req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(c.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("应返回 400，实际 %d", rec.Code)
			}
			if called {
				t.Error("非法端口不应触发落盘回调")
			}
			// 拦下就整个不认：内存里的监听地址与 Web 地址都必须保持原样，
			// 否则界面会回显一个没落盘的非法值。
			if h.Config.Listen != "0.0.0.0:123" {
				t.Errorf("非法请求不应改动内存里的监听地址，实际 %q", h.Config.Listen)
			}
			if h.Config.WebAddr != "" {
				t.Errorf("非法请求不应改动内存里的 Web 地址，实际 %q", h.Config.WebAddr)
			}
		})
	}
}

// TestAPIConfigClientWebAddrStaysLAN 客户端同样只规整主机、端口保持不变
// （否则界面会「自己换端口」）。
func TestAPIConfigClientWebAddrStaysLAN(t *testing.T) {
	h := newTestHandler(t, nil, &Config{ServerAddr: "192.168.1.10:123", Interval: "15m", Step: "5s"})
	h.WebAddr = "127.0.0.1:8090"

	got := doConfig(t, h, http.MethodPost, `{"server":"192.168.1.10:123","interval":"15m","step":"5s"}`)
	if got.WebAddr != "0.0.0.0:8090" {
		t.Fatalf("客户端保存后应为 0.0.0.0:8090，实际 %q", got.WebAddr)
	}
}

// doConfig 直接调用 /api/config 并解析响应。
func doConfig(t *testing.T, h *Handler, method, body string) Config {
	t.Helper()
	mux := http.NewServeMux()
	h.Routes(mux)
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, "/api/config", nil)
	} else {
		req = httptest.NewRequest(method, "/api/config", strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s /api/config 返回 %d：%s", method, rec.Code, rec.Body.String())
	}
	var cfg Config
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("解析响应失败：%v（%s）", err, rec.Body.String())
	}
	return cfg
}

// TestStaticPageNotCached 页面必须禁止缓存。
//
// 界面随程序内嵌，升级后浏览器若沿用旧缓存，用户看到的还是上一版的样子
// （本版已删掉的区块又出现），只会以为升级没生效、反复重装。
func TestStaticPageNotCached(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.staticHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("首页状态码 = %d，期望 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("Cache-Control = %q，期望包含 no-store", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Errorf("Pragma = %q，期望 no-cache", got)
	}
	if !strings.Contains(rec.Body.String(), "SntpServer") {
		t.Error("返回内容不像管理界面首页")
	}
}

// TestIsLoopbackAddr 判断「只对本机开放」的地址，用于安装时提示局域网不可达。
func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{"192.168.1.10:8080", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsLoopbackAddr(c.addr); got != c.want {
			t.Errorf("IsLoopbackAddr(%q) = %v，期望 %v", c.addr, got, c.want)
		}
	}
}

// TestUniquePorts 卸载时要把「配置里的端口」和「历史默认端口」都撤一遍，
// 两边重叠很常见：不去重就会对着同一个端口连删两次（日志里两行一样的），
// 非法端口也必须挡掉——netsh/ufw 收到 0 或 70000 只会白报错。
func TestUniquePorts(t *testing.T) {
	cases := []struct {
		in   []int
		want []int
	}{
		{[]int{123, 123}, []int{123}},
		{[]int{8180, 8080, 123}, []int{8180, 8080, 123}},
		{[]int{0, 70000, 8080, 0}, []int{8080}},
		{nil, nil},
	}
	for _, c := range cases {
		got := UniquePorts(c.in...)
		if len(got) != len(c.want) {
			t.Fatalf("UniquePorts(%v) = %v，期望 %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("UniquePorts(%v) = %v，期望 %v", c.in, got, c.want)
			}
		}
	}
}
