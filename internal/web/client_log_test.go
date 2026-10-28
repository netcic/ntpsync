package web

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"ntpsync/internal/client"
)

// TestClientCheckLog 锁住「检测偏差」日志的可读性：
// 只报偏差数值时看不出本机是快还是慢，也没有往返时延可判断这次测量可不可信。
func TestClientCheckLog(t *testing.T) {
	cases := []struct {
		name   string
		offset time.Duration
		delay  time.Duration
		wants  []string
	}{
		{
			name:   "本地偏慢",
			offset: 252199 * time.Nanosecond,
			delay:  1200 * time.Microsecond,
			wants:  []string{"客户端检测", "服务端 127.0.0.1:123", "252.199µs", "本地偏慢", "往返时延 1.2ms"},
		},
		{
			name:   "本地偏快",
			offset: -8 * time.Second,
			delay:  2 * time.Millisecond,
			wants:  []string{"-8s", "本地偏快"},
		},
		{
			name:   "完全一致",
			offset: 0,
			delay:  time.Millisecond,
			wants:  []string{"偏差 0s", "本地与服务端一致"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := clientCheckLog("127.0.0.1:123", &client.Result{Offset: c.offset, Delay: c.delay})
			for _, w := range c.wants {
				if !strings.Contains(got, w) {
					t.Errorf("日志缺少 %q：%s", w, got)
				}
			}
			assertNoEnglishVerb(t, got)
		})
	}
}

// TestClientSyncLogApplied 已设钟：要能看出调了多少、这次测量准不准。
func TestClientSyncLogApplied(t *testing.T) {
	got := clientSyncLog("10.0.0.5:123", &client.Result{
		Offset:  45 * time.Millisecond,
		Delay:   900 * time.Microsecond,
		Applied: true,
	})
	for _, w := range []string{"客户端校时", "服务端 10.0.0.5:123", "已设钟", "45ms", "本地偏慢", "往返时延 900µs"} {
		if !strings.Contains(got, w) {
			t.Errorf("日志缺少 %q：%s", w, got)
		}
	}
	assertNoEnglishVerb(t, got)
}

// TestClientSyncLogSkipped 被阈值拦下：原因要原样带上，不能只剩「未设钟」。
func TestClientSyncLogSkipped(t *testing.T) {
	reason := "偏差 17s 超过阈值 5s，已拒绝自动设钟（防止时间源异常污染本机）"
	got := clientSyncLog("10.0.0.5:123", &client.Result{
		Offset:  17 * time.Second,
		Delay:   time.Millisecond,
		Skipped: true,
		Reason:  reason,
	})
	if !strings.Contains(got, "未设钟") || !strings.Contains(got, reason) {
		t.Fatalf("应保留跳过原因：%s", got)
	}
	// 原因里已经写了偏差与阈值，正文再拼一遍 offset 就是重复信息。
	if strings.Count(got, "偏差") != 1 {
		t.Errorf("偏差只应出现一次（原因内），实际：%s", got)
	}
	assertNoEnglishVerb(t, got)
}

// TestClientSyncLogNoReason 没给原因时也要能说清「偏差多少、没动钟」。
func TestClientSyncLogNoReason(t *testing.T) {
	got := clientSyncLog("10.0.0.5:123", &client.Result{Offset: 3 * time.Millisecond})
	if !strings.Contains(got, "偏差 3ms") || !strings.Contains(got, "未调整") {
		t.Fatalf("无原因时应补偏差与未调整：%s", got)
	}
}

// assertNoEnglishVerb 断言日志正文里没有 check/sync 这类英文动词，
// 也不残留 key=value 式的调试字段（老版本日志曾把空值字段全打出来）。
func assertNoEnglishVerb(t *testing.T, line string) {
	t.Helper()
	for _, bad := range []string{"check", "sync", "applied=", "skipped=", "reason="} {
		if strings.Contains(line, bad) {
			t.Errorf("日志里不该出现 %q：%s", bad, line)
		}
	}
}

// TestDescribeClientErr 失败原因要能被照着重试：
// 原始错误里地址重复两次、带 client: 包名前缀和 read udp 的 socket 细节，
// 混在中文日志里没人看得懂。
func TestDescribeClientErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "超时无应答",
			err:  fmt.Errorf("client: 请求服务端 1.2.3.4:123 失败: %w", &net.DNSError{Err: "i/o timeout", IsTimeout: true}),
			want: "超时无应答",
		},
		{
			name: "连接被拒",
			err:  errors.New("client: 请求服务端 1.2.3.4:123 失败: read udp 1.2.3.4:123: connection refused"),
			want: "连接被拒绝",
		},
		{
			name: "主机名解析失败",
			err:  errors.New("client: 请求服务端 ntp.bad:123 失败: lookup ntp.bad: no such host"),
			want: "地址解析失败",
		},
		{
			name: "地址缺端口",
			err:  errors.New("client: 地址 1.2.3.4 无效: address 1.2.3.4: missing port in address"),
			want: "地址格式不对",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := describeClientErr(c.err)
			if !strings.Contains(got, c.want) {
				t.Fatalf("应含 %q，实际：%s", c.want, got)
			}
			for _, bad := range []string{"client:", "read udp", "i/o timeout", "1.2.3.4:123:123"} {
				if strings.Contains(got, bad) {
					t.Errorf("日志不该出现 %q：%s", bad, got)
				}
			}
		})
	}
	if got := describeClientErr(nil); got != "" {
		t.Errorf("nil 错误应返回空串，实际 %q", got)
	}
}
