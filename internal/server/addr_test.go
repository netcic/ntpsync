package server

import (
	"net"
	"strings"
	"testing"
)

// TestNormalizeListenAddr 校验监听地址的展示归一化：
// 通配地址（0.0.0.0 / :: / nil）一律显示为 0.0.0.0:port，
// 具体地址原样保留（IPv6 保持方括号写法）。
func TestNormalizeListenAddr(t *testing.T) {
	cases := []struct {
		name string
		in   net.Addr
		want string
	}{
		{"IPv6 通配", &net.UDPAddr{IP: net.IPv6unspecified, Port: 123}, "0.0.0.0:123"},
		{"IPv4 通配", &net.UDPAddr{IP: net.IPv4zero, Port: 123}, "0.0.0.0:123"},
		{"nil IP", &net.UDPAddr{IP: nil, Port: 123}, "0.0.0.0:123"},
		{"具体 IPv4", &net.UDPAddr{IP: net.ParseIP("192.168.1.10"), Port: 123}, "192.168.1.10:123"},
		{"回环 IPv4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 123}, "127.0.0.1:123"},
		{"具体 IPv6", &net.UDPAddr{IP: net.ParseIP("::1"), Port: 123}, "[::1]:123"},
		{"非 123 端口", &net.UDPAddr{IP: net.IPv6unspecified, Port: 8123}, "0.0.0.0:8123"},
	}
	for _, c := range cases {
		if got := normalizeListenAddr(c.in); got != c.want {
			t.Errorf("%s: normalizeListenAddr(%v) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	// nil Addr 不应 panic。
	if got := normalizeListenAddr(nil); got != "" {
		t.Errorf("nil addr 应返回空串，实际 %q", got)
	}
}

// TestDisplayAddrWildcard 真实绑定 0.0.0.0:0，断言：
// DisplayAddr 不含 "[::]"（归一化生效），LocalAddr 保持真实值语义。
func TestDisplayAddrWildcard(t *testing.T) {
	s := New(Config{Listen: "0.0.0.0:0", Stratum: DefaultStratum})
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	disp := s.DisplayAddr()
	if !strings.HasPrefix(disp, "0.0.0.0:") {
		t.Fatalf("DisplayAddr 应以 0.0.0.0: 开头，实际 %q", disp)
	}
	if strings.Contains(disp, "[") || strings.Contains(disp, "::") {
		t.Fatalf("DisplayAddr 不应含 IPv6 通配写法，实际 %q", disp)
	}
	// 端口应是系统分配的真实端口（非 0）。
	if strings.HasSuffix(disp, ":0") {
		t.Fatalf("DisplayAddr 端口不应为 0，实际 %q", disp)
	}

	// LocalAddr 保持真实地址语义（可 dial），这里只断言非空。
	if s.LocalAddr() == "" {
		t.Fatal("LocalAddr 不应为空")
	}
}

// TestDisplayAddrSpecific 绑定具体 IP 时不应被归一化改写。
func TestDisplayAddrSpecific(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0", Stratum: DefaultStratum})
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	disp := s.DisplayAddr()
	if !strings.HasPrefix(disp, "127.0.0.1:") {
		t.Fatalf("绑定具体 IP 时应原样展示，实际 %q", disp)
	}
}

// TestDisplayAddrNotStarted 未启动时 DisplayAddr 返回空串。
func TestDisplayAddrNotStarted(t *testing.T) {
	s := New(Config{Listen: "0.0.0.0:0"})
	if got := s.DisplayAddr(); got != "" {
		t.Errorf("未启动应返回空串，实际 %q", got)
	}
}

// TestLocalIPs 校验本机 IPv4 地址列表：不含回环与链路本地地址，且无重复。
func TestLocalIPs(t *testing.T) {
	ips := LocalIPs()
	seen := make(map[string]bool)
	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Errorf("返回了非法 IP: %q", ip)
			continue
		}
		if parsed.IsLoopback() {
			t.Errorf("不应包含回环地址: %s", ip)
		}
		if parsed.IsLinkLocalUnicast() {
			t.Errorf("不应包含链路本地地址: %s", ip)
		}
		if parsed.To4() == nil {
			t.Errorf("应为 IPv4 地址: %s", ip)
		}
		if seen[ip] {
			t.Errorf("地址重复: %s", ip)
		}
		seen[ip] = true
	}
}
