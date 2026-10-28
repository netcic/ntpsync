package ntp

import (
	"testing"
	"time"
)

func TestTimeToNTP_RoundTrip(t *testing.T) {
	cases := []time.Time{
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 6, 15, 12, 30, 45, 0, time.UTC),
		time.Date(1999, 12, 31, 23, 59, 59, 0, time.UTC), // 仍晚于 NTP 纪元
		time.Date(2024, 1, 1, 0, 0, 0, 123456789, time.UTC),
	}
	for _, tc := range cases {
		got := TimeToNTP(tc).ToTime()
		diff := got.Sub(tc)
		if diff < -time.Microsecond || diff > time.Microsecond {
			t.Errorf("round-trip 偏差过大: 输入 %v 得到 %v (差 %v)", tc, got, diff)
		}
	}
}

func TestPacket_MarshalParseRoundTrip(t *testing.T) {
	now := time.Date(2024, 6, 15, 12, 30, 45, 0, time.UTC)
	req := NewClientRequest()
	req.TransmitTime = TimeToNTP(now)

	resp := BuildServerResponse(req, now.Add(2*time.Millisecond), 2)
	resp.RootDelay = 0x00010000 // 1.0 秒（16.16）
	resp.RootDispersion = 0x00008000

	raw := resp.Marshal()
	if len(raw) != packetSize {
		t.Fatalf("Marshal 长度应为 %d，实际 %d", packetSize, len(raw))
	}

	got, err := ParsePacket(raw)
	if err != nil {
		t.Fatalf("ParsePacket 失败: %v", err)
	}

	if got.Mode != 4 {
		t.Errorf("Mode 应为 4，实际 %d", got.Mode)
	}
	if got.Stratum != 2 {
		t.Errorf("Stratum 应为 2，实际 %d", got.Stratum)
	}
	if got.ReferenceID != 0x4C4F434C {
		t.Errorf("ReferenceID 应为 LOCL，实际 %#x", got.ReferenceID)
	}
	if got.RootDelay != 0x00010000 {
		t.Errorf("RootDelay 回显失败: %#x", got.RootDelay)
	}
	if got.RootDispersion != 0x00008000 {
		t.Errorf("RootDispersion 回显失败: %#x", got.RootDispersion)
	}
	// Originate 应原样回显客户端的 TransmitTime（T1）
	if got.OriginateTime != req.TransmitTime {
		t.Errorf("Originate 未回显 T1: 期望 %v 实际 %v", req.TransmitTime, got.OriginateTime)
	}
	// TransmitTime 应等于服务端 now（约 now+2ms 的截断）
	if got.TransmitTime.ToTime().Sub(now.Add(2*time.Millisecond)) > time.Millisecond {
		t.Errorf("TransmitTime 异常: %v", got.TransmitTime.ToTime())
	}
}

func TestComputeOffsetDelay(t *testing.T) {
	// 构造场景：客户端相对服务端慢 8ms，单程时延 10ms（对称）。
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(18 * time.Millisecond) // 服务端收：8ms 偏差 + 10ms 单程
	t3 := t2
	t4 := base.Add(20 * time.Millisecond) // 客户端收：t3 + 10ms 单程

	offset, delay := ComputeOffsetDelay(t1, t2, t3, t4)
	if offset != 8*time.Millisecond {
		t.Errorf("offset 应为 8ms，实际 %v", offset)
	}
	if delay != 20*time.Millisecond {
		t.Errorf("delay 应为 20ms，实际 %v", delay)
	}
}

func TestComputeOffsetDelay_Asymmetric(t *testing.T) {
	// 非对称单程：上行 5ms、下行 15ms，偏差 0。
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := base
	t2 := base.Add(5 * time.Millisecond)
	t3 := base.Add(5 * time.Millisecond)
	t4 := base.Add(20 * time.Millisecond) // 下行 15ms

	offset, delay := ComputeOffsetDelay(t1, t2, t3, t4)
	// offset = ((5)+(5-20))/2 = (5-15)/2 = -5ms
	if offset != -5*time.Millisecond {
		t.Errorf("offset 应为 -5ms，实际 %v", offset)
	}
	// delay = (20) - (0) = 20ms
	if delay != 20*time.Millisecond {
		t.Errorf("delay 应为 20ms，实际 %v", delay)
	}
}

func TestParsePacket_TooShort(t *testing.T) {
	if _, err := ParsePacket(make([]byte, 10)); err == nil {
		t.Error("长度不足时应返回错误")
	}
}

func TestClientOffsetDelay_FromResponse(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := base.Add(100 * time.Millisecond)
	t4 := base.Add(200 * time.Millisecond)

	resp := &Packet{
		ReceiveTime:  TimeToNTP(base.Add(115 * time.Millisecond)), // T2
		TransmitTime: TimeToNTP(base.Add(115 * time.Millisecond)), // T3
	}
	const eps = time.Microsecond // NTP 时间戳分辨率约 232ps，允许亚微秒取整误差
	offset, delay := resp.ClientOffsetDelay(t1, t4)
	// offset = ((15)+(115-200))/2 = (15-85)/2 = -35ms
	if offset < -35*time.Millisecond-eps || offset > -35*time.Millisecond+eps {
		t.Errorf("offset 应为约 -35ms，实际 %v", offset)
	}
	// delay = (100)-(0) = 100ms
	if delay < 100*time.Millisecond-eps || delay > 100*time.Millisecond+eps {
		t.Errorf("delay 应为约 100ms，实际 %v", delay)
	}
}
