package server

import (
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"ntpsync/internal/ntp"
)

// TestServerLoopbackQuery 端到端冒烟：
// 本地回环启动服务端 → 用 ntp.Query 客户端请求 → 断言拿到合法响应且偏差接近 0。
func TestServerLoopbackQuery(t *testing.T) {
	// 用随机空闲端口，避免与系统真实 NTP(123) 冲突，也无需提权。
	s := New(Config{Listen: "127.0.0.1:0", Stratum: DefaultStratum})
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	// 拿到实际监听端口。
	s.mu.Lock()
	addr := s.conn.LocalAddr().String()
	s.mu.Unlock()

	offset, delay, err := ntp.Query(addr, 2*time.Second)
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	// 回环链路，偏差应接近 0（本地处理耗时极小）。
	if offset < -50*time.Millisecond || offset > 50*time.Millisecond {
		t.Errorf("loopback offset 应接近 0，实际 %v", offset)
	}
	// delay 可能是 0（回环往返低于时钟分辨率），但不能是异常负值。
	if delay < -time.Millisecond {
		t.Errorf("delay 异常: %v", delay)
	}
	// 服务端应记录到至少 1 次请求。
	if s.Requests() < 1 {
		t.Errorf("服务端应统计到请求，实际 %d", s.Requests())
	}
	if s.LastPeer() == "" {
		t.Errorf("服务端应记录最近客户端地址")
	}
	t.Logf("offset=%v delay=%v requests=%d peer=%s", offset, delay, s.Requests(), s.LastPeer())
}

// TestServerResponseFields 直连收发，验证响应报文字段合法性。
func TestServerResponseFields(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0", Stratum: 2})
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	s.mu.Lock()
	addr := s.conn.LocalAddr().String()
	s.mu.Unlock()

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("Dial 失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write(ntp.NewClientRequest().Marshal()); err != nil {
		t.Fatalf("Write 失败: %v", err)
	}
	buf := make([]byte, 48)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	p, err := ntp.ParsePacket(buf[:n])
	if err != nil {
		t.Fatalf("ParsePacket 失败: %v", err)
	}
	if p.Mode != 4 {
		t.Errorf("响应 Mode 应为 4（服务端），实际 %d", p.Mode)
	}
	if p.Stratum != 2 {
		t.Errorf("Stratum 应为 2，实际 %d", p.Stratum)
	}
	if p.TransmitTime.Seconds == 0 {
		t.Errorf("TransmitTime 不应为 0")
	}
}

// TestServerStartStop 验证启停状态机。
func TestServerStartStop(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	if s.IsRunning() {
		t.Fatal("新实例不应在运行")
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if !s.IsRunning() {
		t.Fatal("Start 后应处于运行态")
	}
	if err := s.Start(); err == nil {
		t.Fatal("重复 Start 应报错")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if s.IsRunning() {
		t.Fatal("Stop 后不应处于运行态")
	}
}

// TestServerConcurrency 并发健壮性（覆盖 R9）：
// 多个客户端 goroutine 同时请求，验证服务端无丢包、计数准确、响应均合法。
func TestServerConcurrency(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0", Stratum: 2})
	if err := s.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer s.Stop()

	addr := s.LocalAddr()
	const clients = 50
	const each = 5 // 每客户端 5 次，共 250 次请求

	var wg sync.WaitGroup
	errCh := make(chan error, clients*each)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				off, delay, err := ntp.Query(addr, 3*time.Second)
				if err != nil {
					errCh <- err
					return
				}
				if off < -100*time.Millisecond || off > 100*time.Millisecond {
					errCh <- &offsetErr{off}
					return
				}
				if delay < -time.Millisecond {
					errCh <- &delayErr{delay}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("并发请求异常: %v", err)
	}

	// 服务端计数应等于总请求数（无丢包）。
	if got := s.Requests(); got != clients*each {
		t.Errorf("服务端请求计数 = %d，应为 %d", got, clients*each)
	}
}

type offsetErr struct{ off time.Duration }

func (e *offsetErr) Error() string { return "offset 越界: " + e.off.String() }

type delayErr struct{ delay time.Duration }

func (e *delayErr) Error() string { return "delay 异常: " + e.delay.String() }

// TestNoteClientAggregatesByIP 锁住用户列表的核心契约：
// 同一 IP 的不同源端口必须合并成一行，授时次数要累加（而不是每行都是 1）。
//
// 真实客户端每轮校时都会换源端口（Go 客户端每轮新建 socket，
// Windows 客户端每次 daemon 循环亦然），按 ip:port 记账就会刷出一屏重复行。
func TestNoteClientAggregatesByIP(t *testing.T) {
	s := New(Config{})
	base := time.Now()

	// 同一台客户端：三次授时，三个不同源端口。
	for i, port := range []int{60150, 50032, 51236} {
		s.noteClient(&net.UDPAddr{IP: net.ParseIP("192.168.1.20"), Port: port},
			base.Add(time.Duration(i)*time.Minute))
	}
	// 另一台客户端：一次授时。
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("192.168.1.21"), Port: 40000}, base)

	list := s.Clients()
	if len(list) != 2 {
		t.Fatalf("应按 IP 聚合成 2 行，实际 %d 行：%+v", len(list), list)
	}
	// 最近授时的排最前：.20 的最后一次是 base+2m，晚于 .21 的 base。
	first := list[0]
	if first.Addr != "192.168.1.20" {
		t.Fatalf("首行应为 192.168.1.20，实际 %q", first.Addr)
	}
	if first.Count != 3 {
		t.Errorf("同一 IP 三次授时应累加为 3，实际 %d", first.Count)
	}
	if !first.FirstSee.Equal(base) {
		t.Errorf("首次授时应保持第一次的时刻 %v，实际 %v", base, first.FirstSee)
	}
	if want := base.Add(2 * time.Minute); !first.LastSee.Equal(want) {
		t.Errorf("最近授时应为最后一次的时刻 %v，实际 %v", want, first.LastSee)
	}
	if second := list[1]; second.Addr != "192.168.1.21" || second.Count != 1 {
		t.Errorf("第二行应为 192.168.1.21 且次数 1，实际 %+v", second)
	}
}

// TestNoteClientEvictionByIP 验证超过上限时按「首次授时」淘汰最早的 IP。
func TestNoteClientEvictionByIP(t *testing.T) {
	s := New(Config{})
	base := time.Now()

	// 造 maxClients+5 个不同 IP，依次接入。
	n := maxClients + 5
	for i := 0; i < n; i++ {
		s.noteClient(&net.UDPAddr{IP: net.ParseIP("10.0.0." + strconv.Itoa(i+1)), Port: 30000},
			base.Add(time.Duration(i)*time.Second))
	}
	list := s.Clients()
	if len(list) != maxClients {
		t.Fatalf("列表应裁剪到 %d 个 IP，实际 %d", maxClients, len(list))
	}
	for _, ci := range list {
		if ci.Addr == "10.0.0.1" {
			t.Fatal("最早接入的 10.0.0.1 应被淘汰")
		}
	}
	if len(s.order) != maxClients {
		t.Errorf("淘汰队列长度应为 %d，实际 %d", maxClients, len(s.order))
	}
}
