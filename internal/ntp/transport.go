package ntp

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// lookupIP 用于域名解析，抽成变量便于测试注入固定结果。
var lookupIP = net.LookupIP

// HandleRequest 处理一个收到的 NTP 请求字节，返回响应字节。
// 设计为纯函数，便于脱离真实网络做单元测试。
func HandleRequest(reqBytes []byte, now time.Time, stratum uint8) ([]byte, error) {
	req, err := ParsePacket(reqBytes)
	if err != nil {
		return nil, err
	}
	if req.Mode != 3 {
		return nil, fmt.Errorf("ntp: 收到非客户端请求报文 mode=%d", req.Mode)
	}
	resp := BuildServerResponse(req, now, stratum)
	return resp.Marshal(), nil
}

// ServeUDP 在 conn 上循环处理 NTP 请求（阻塞）。由调用方负责关闭 conn 以退出。
func ServeUDP(conn *net.UDPConn, stratum uint8) error {
	buf := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err // conn 被关闭时退出
		}
		if n < packetSize {
			continue
		}
		resp, err := HandleRequest(buf[:n], time.Now(), stratum)
		if err != nil {
			continue
		}
		_, _ = conn.WriteToUDP(resp, addr)
	}
}

// Query 向 addr（如 "127.0.0.1:123"、"ntp.aliyun.com"）发送 NTP 请求，
// 返回时钟偏差与时延。
//
// 为什么需要「多地址并发探测」：一个域名常解析出多个 A 记录，而各节点健康状态
// 未必一致（实测 ntp.ntsc.ac.cn 的 3 个 IP 中只有 1 个响应）。UDP 是无连接的，
// Dial 只要 socket 建好就算成功，无法感知对端是否活着，因此单地址写法会卡在
// 故障节点上一直等到超时，只能靠运气重试。这里对解析出的每个 IP 并发发一次
// 请求，取最先返回的有效响应，从而在部分节点故障时依然可用。
func Query(addr string, timeout time.Duration) (offset, delay time.Duration, err error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, 0, err
	}

	// IP 字面量无需解析，直接查询（保留原始错误，便于排查）。
	if ip := net.ParseIP(host); ip != nil {
		return queryOne(addr, timeout)
	}

	ips, err := lookupIP(host)
	if err != nil {
		return 0, 0, fmt.Errorf("ntp: 解析服务器 %s 失败: %w", host, err)
	}
	targets := joinTargets(ips, port)
	if len(targets) == 0 {
		return 0, 0, fmt.Errorf("ntp: 服务器 %s 未解析出任何 IP 地址", host)
	}

	// 单地址：不必并发，直接查。
	if len(targets) == 1 {
		return queryOne(targets[0], timeout)
	}

	type probe struct {
		addr   string
		offset time.Duration
		delay  time.Duration
		err    error
	}
	// 带缓冲，保证先返回后其余 goroutine 写入不会阻塞泄漏。
	ch := make(chan probe, len(targets))
	for _, t := range targets {
		go func(target string) {
			o, d, e := queryOne(target, timeout)
			ch <- probe{addr: target, offset: o, delay: d, err: e}
		}(t)
	}

	var errs []string
	for i := 0; i < len(targets); i++ {
		r := <-ch
		if r.err == nil {
			return r.offset, r.delay, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", r.addr, r.err))
	}
	return 0, 0, fmt.Errorf("ntp: 服务器 %s 的 %d 个地址全部无响应（%s）",
		host, len(targets), strings.Join(errs, "；"))
}

// joinTargets 把解析结果拼成 "host:port"，并去重、保持原顺序。
func joinTargets(ips []net.IP, port string) []string {
	seen := make(map[string]bool, len(ips))
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		t := net.JoinHostPort(ip.String(), port)
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// maxProbeAttempts 为单地址最大尝试次数。
//
// NTP 走 UDP，丢包是常态而非异常：实测某国家授时节点对同一源主机的丢包率
// 可达 75%（4 次请求仅 1 次有响应）。只发一次包就把「无响应」判定为「不可用」
// 会严重误判，故对超时类失败重试。三次尝试的总预算仍控制在调用方给的 timeout 内。
const maxProbeAttempts = 3

// minProbePerTry 为单次尝试的超时下限，避免调用方给的 timeout 过小时
// 单次窗口短到连局域网往返都容不下。
const minProbePerTry = 200 * time.Millisecond

// queryOne 向单个具体地址（host:port，host 为 IP 字面量）完成一次校时，
// 内部按 maxProbeAttempts 重试超时类失败。
func queryOne(addr string, timeout time.Duration) (offset, delay time.Duration, err error) {
	perTry := timeout / maxProbeAttempts
	if perTry < minProbePerTry {
		perTry = minProbePerTry
	}

	var lastErr error
	for i := 0; i < maxProbeAttempts; i++ {
		offset, delay, err = queryOnce(addr, perTry)
		if err == nil {
			return offset, delay, nil
		}
		lastErr = err
		// 非超时类错误（如地址不可达、报文非法）重试无意义，直接放弃。
		if ne, ok := err.(net.Error); ok && !ne.Timeout() {
			break
		}
	}
	return 0, 0, lastErr
}

// queryOnce 完成单个地址的一次请求-响应往返。
func queryOnce(addr string, timeout time.Duration) (offset, delay time.Duration, err error) {
	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// T1：客户端发起时刻
	t1 := time.Now()
	req := NewClientRequest()
	req.TransmitTime = TimeToNTP(t1)
	if _, err = conn.Write(req.Marshal()); err != nil {
		return 0, 0, err
	}

	buf := make([]byte, packetSize)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, 0, err
	}
	// T4：客户端收包时刻
	t4 := time.Now()
	p, err := ParsePacket(buf[:n])
	if err != nil {
		return 0, 0, err
	}
	offset, delay = p.ClientOffsetDelay(t1, t4)
	return offset, delay, nil
}
