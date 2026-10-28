package ntp

import (
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startTestServer 起一个真实的本地 NTP 响应服务，返回其 port。
func startTestServer(t *testing.T) (port string, cleanup func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	conn := pc.(*net.UDPConn)
	go func() { _ = ServeUDP(conn, 2) }()
	_, p, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		conn.Close()
		t.Fatalf("解析本地端口失败: %v", err)
	}
	return p, func() { conn.Close() }
}

// stubLookup 临时替换域名解析函数，返回预设 IP 列表。
func stubLookup(t *testing.T, ips []net.IP, err error) {
	t.Helper()
	orig := lookupIP
	lookupIP = func(string) ([]net.IP, error) { return ips, err }
	t.Cleanup(func() { lookupIP = orig })
}

// deadIP 为 RFC5737 保留的文档测试网段地址，保证不会有真实主机响应。
const deadIP = "192.0.2.1"

// TestQuery_MultiIP_FallbackToAlive 是本修复的核心回归测试：
// 域名解析出多个 IP 且其中部分故障时，仍应成功校时
// （对应线上问题：ntp.ntsc.ac.cn 的 3 个 IP 里只有 1 个响应）。
func TestQuery_MultiIP_FallbackToAlive(t *testing.T) {
	port, cleanup := startTestServer(t)
	defer cleanup()

	// 故意把死 IP 排在前面，模拟 DNS 轮询先返回故障节点。
	stubLookup(t, []net.IP{
		net.ParseIP(deadIP),
		net.ParseIP("127.0.0.1"),
	}, nil)

	start := time.Now()
	offset, _, err := Query(net.JoinHostPort("example.invalid", port), 500*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("坏 IP 在前时应回退到可用 IP 并成功，实际报错: %v", err)
	}
	if offset < -10*time.Millisecond || offset > 10*time.Millisecond {
		t.Errorf("offset 应接近 0，实际 %v", offset)
	}
	// 并发探测：不应等待死 IP 全额超时后才返回。
	if elapsed > 400*time.Millisecond {
		t.Errorf("应为并发探测（取最快响应），实际耗时 %v 过长", elapsed)
	}
	t.Logf("多 IP 回退成功: offset=%v 耗时=%v", offset, elapsed)
}

// TestQuery_RetryOnLossyLink 模拟高丢包链路：服务端只回应第 3 个报文。
// 对应线上实况（某国家授时节点对同一源主机丢包率约 75%），单次发包必然误判。
func TestQuery_RetryOnLossyLink(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	conn := pc.(*net.UDPConn)
	defer conn.Close()

	var got int32
	go func() {
		buf := make([]byte, 1024)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < packetSize {
				continue
			}
			// 前两个报文静默丢弃，模拟丢包。
			if atomic.AddInt32(&got, 1) < 3 {
				continue
			}
			resp, err := HandleRequest(buf[:n], time.Now(), 2)
			if err != nil {
				continue
			}
			_, _ = conn.WriteToUDP(resp, addr)
		}
	}()

	_, port, _ := net.SplitHostPort(conn.LocalAddr().String())
	start := time.Now()
	offset, _, err := Query(net.JoinHostPort("127.0.0.1", port), 900*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("高丢包链路应重试至成功，实际报错: %v", err)
	}
	if offset < -10*time.Millisecond || offset > 10*time.Millisecond {
		t.Errorf("offset 应接近 0，实际 %v", offset)
	}
	if n := atomic.LoadInt32(&got); n < 3 {
		t.Errorf("应至少发出 3 个报文，实际 %d", n)
	}
	t.Logf("高丢包重试成功: 收到第 %d 个报文才响应，耗时 %v", atomic.LoadInt32(&got), elapsed)
}

// TestQuery_MultiIP_AllDead 校验全部地址不可用时错误信息足以定位问题。
func TestQuery_MultiIP_AllDead(t *testing.T) {
	stubLookup(t, []net.IP{
		net.ParseIP(deadIP),
		net.ParseIP("192.0.2.2"),
	}, nil)

	_, _, err := Query(net.JoinHostPort("dead.invalid", "9999"), 200*time.Millisecond)
	if err == nil {
		t.Fatal("全部地址不可用时应当报错")
	}
	msg := err.Error()
	for _, want := range []string{"全部无响应", deadIP, "192.0.2.2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应包含 %q，实际: %s", want, msg)
		}
	}
	t.Logf("全死错误信息: %v", err)
}

// TestQuery_LookupError 校验解析失败时给出明确原因。
func TestQuery_LookupError(t *testing.T) {
	stubLookup(t, nil, errors.New("no such host"))

	_, _, err := Query("nope.invalid:123", time.Second)
	if err == nil {
		t.Fatal("解析失败时应当报错")
	}
	if !strings.Contains(err.Error(), "解析服务器") {
		t.Errorf("错误信息应说明解析失败，实际: %v", err)
	}
}

// TestQuery_EmptyLookupResult 校验解析成功但无地址的边界情况。
func TestQuery_EmptyLookupResult(t *testing.T) {
	stubLookup(t, nil, nil)

	_, _, err := Query("empty.invalid:123", time.Second)
	if err == nil {
		t.Fatal("无可用地址时应当报错")
	}
	if !strings.Contains(err.Error(), "未解析出任何 IP") {
		t.Errorf("错误信息应说明无 IP，实际: %v", err)
	}
}

// TestQuery_IPLiteralSkipsDNS 校验 IP 字面量不走域名解析（避免无谓查询）。
func TestQuery_IPLiteralSkipsDNS(t *testing.T) {
	port, cleanup := startTestServer(t)
	defer cleanup()

	called := false
	orig := lookupIP
	lookupIP = func(string) ([]net.IP, error) { called = true; return nil, nil }
	t.Cleanup(func() { lookupIP = orig })

	if _, _, err := Query(net.JoinHostPort("127.0.0.1", port), time.Second); err != nil {
		t.Fatalf("IP 字面量查询失败: %v", err)
	}
	if called {
		t.Error("IP 字面量不应触发域名解析")
	}
}

// TestJoinTargets 校验地址拼接的去重与 IPv6 括号处理。
func TestJoinTargets(t *testing.T) {
	ips := []net.IP{
		net.ParseIP("10.0.0.1"),
		net.ParseIP("10.0.0.1"), // 重复应被去掉
		nil,                     // 空值应跳过
		net.ParseIP("2001:db8::1"),
	}
	got := joinTargets(ips, "123")
	want := []string{"10.0.0.1:123", "[2001:db8::1]:123"}
	if len(got) != len(want) {
		t.Fatalf("期望 %d 个目标，实际 %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个目标期望 %q，实际 %q", i, want[i], got[i])
		}
	}
}

func TestHandleRequest_BuildsResponse(t *testing.T) {
	req := NewClientRequest()
	req.TransmitTime = TimeToNTP(time.Now())

	resp, err := HandleRequest(req.Marshal(), time.Now(), 3)
	if err != nil {
		t.Fatalf("HandleRequest 失败: %v", err)
	}
	p, err := ParsePacket(resp)
	if err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if p.Mode != 4 {
		t.Errorf("Mode 应为 4，实际 %d", p.Mode)
	}
	if p.Stratum != 3 {
		t.Errorf("Stratum 应为 3，实际 %d", p.Stratum)
	}
	if p.OriginateTime != req.TransmitTime {
		t.Errorf("Originate 未原样回显客户端 T1")
	}
}

func TestServerClient_Loopback(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	defer pc.Close()
	conn := pc.(*net.UDPConn)

	go func() { _ = ServeUDP(conn, 2) }()

	addr := conn.LocalAddr().String()
	offset, delay, err := Query(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	// 回环往返极快，delay 可能因时钟分辨率被取整为 0；仅拒绝明显异常负值
	if delay < -time.Millisecond {
		t.Errorf("delay 异常为负: %v", delay)
	}
	// 回环链路偏差应接近 0（本地处理耗时极小）
	if offset < -10*time.Millisecond || offset > 10*time.Millisecond {
		t.Errorf("loopback offset 应接近 0，实际 %v", offset)
	}
	t.Logf("loopback: offset=%v delay=%v", offset, delay)
}
