// Package ntp 实现 NTP/SNTP 核心能力：
//   - 64 位 NTP 时间戳（秒 + 小数）与 time.Time 互转；
//   - RFC5905 固定 48 字节报文的编解码；
//   - 客户端偏差/时延计算；
//   - 服务端响应报文构造。
//
// 仅依赖标准库，可在 Windows / Linux / 国产 OS（同内核）跨平台复用。
package ntp

import (
	"encoding/binary"
	"errors"
	"time"
)

// ntpEpochOffset 是 NTP 纪元（1900-01-01T00:00:00Z）相对 Unix 纪元的秒差。
const ntpEpochOffset int64 = 2208988800

// packetSize 为 NTP 固定报文长度（不含扩展/认证字段）。
const packetSize = 48

// NTPTime 表示 64 位 NTP 时间戳：高 32 位为秒，低 32 位为秒的小数（2^-32 秒，约 232ps）。
type NTPTime struct {
	Seconds  uint32
	Fraction uint32
}

// TimeToNTP 将 time.Time 转为 NTPTime（截断到 NTP 精度）。
func TimeToNTP(t time.Time) NTPTime {
	t = t.UTC()
	sec := uint32(t.Unix() + ntpEpochOffset)
	// 小数部分：纳秒 / 1e9 * 2^32，用整数运算避免浮点误差。
	frac := uint32((uint64(t.Nanosecond()) << 32) / 1_000_000_000)
	return NTPTime{Seconds: sec, Fraction: frac}
}

// ToTime 将 NTPTime 转回 time.Time（UTC）。
func (n NTPTime) ToTime() time.Time {
	unixSec := int64(n.Seconds) - ntpEpochOffset
	nsec := int64((uint64(n.Fraction) * 1_000_000_000) >> 32)
	return time.Unix(unixSec, nsec).UTC()
}

// Packet 为 NTP 报文（RFC5905）的固定 48 字节部分（无扩展/认证）。
type Packet struct {
	LI             uint8   // 2 bits 闰秒指示
	VN             uint8   // 3 bits 版本号
	Mode           uint8   // 3 bits 模式：3=客户端，4=服务端
	Stratum        uint8   // 层级（1=主钟，0=未同步）
	Poll           int8    // 轮询间隔指数
	Precision      int8    // 时钟精度指数（负）
	RootDelay      uint32  // 16.16 定点：到主钟的总延迟
	RootDispersion uint32  // 16.16 定点：最大误差
	ReferenceID    uint32  // 参考源标识
	ReferenceTime  NTPTime // 上次设钟/校准时刻
	OriginateTime  NTPTime // 客户端发起时刻 T1
	ReceiveTime    NTPTime // 服务端收到时刻 T2
	TransmitTime   NTPTime // 服务端发出时刻 T3（客户端请求时为 T1）
}

// NewClientRequest 构造一个客户端请求报文，TransmitTime 填入当前时刻（即 T1）。
func NewClientRequest() *Packet {
	return &Packet{
		LI:           0,
		VN:           4,
		Mode:         3,
		Stratum:      0,
		Poll:         0,
		Precision:    0,
		TransmitTime: TimeToNTP(time.Now()),
	}
}

// BuildServerResponse 基于收到的客户端请求构造服务端响应。
// serverNow 用于填充 Reference/Receive/Transmit；Originate 原样回显客户端的 T1。
func BuildServerResponse(req *Packet, serverNow time.Time, stratum uint8) *Packet {
	return &Packet{
		LI:             req.LI,
		VN:             req.VN,
		Mode:           4, // 服务端模式
		Stratum:        stratum,
		Poll:           req.Poll,
		Precision:      -20, // 约 2^-20 s ≈ 1us 精度
		RootDelay:      0,
		RootDispersion: 0,
		ReferenceID:    0x4C4F434C, // "LOCL"，表示本地时钟源
		ReferenceTime:  TimeToNTP(serverNow),
		OriginateTime:  req.TransmitTime, // 回显客户端 T1
		ReceiveTime:    TimeToNTP(serverNow),
		TransmitTime:   TimeToNTP(serverNow),
	}
}

// Marshal 将报文序列化为 48 字节（大端）。
func (p *Packet) Marshal() []byte {
	b := make([]byte, packetSize)
	b[0] = (p.LI&0x3)<<6 | (p.VN&0x7)<<3 | (p.Mode & 0x7)
	b[1] = p.Stratum
	b[2] = byte(p.Poll)
	b[3] = byte(p.Precision)
	binary.BigEndian.PutUint32(b[4:8], p.RootDelay)
	binary.BigEndian.PutUint32(b[8:12], p.RootDispersion)
	binary.BigEndian.PutUint32(b[12:16], p.ReferenceID)
	putNTP(b[16:24], p.ReferenceTime)
	putNTP(b[24:32], p.OriginateTime)
	putNTP(b[32:40], p.ReceiveTime)
	putNTP(b[40:48], p.TransmitTime)
	return b
}

// ParsePacket 解析 48 字节（或更长）的 NTP 报文固定部分。
func ParsePacket(b []byte) (*Packet, error) {
	if len(b) < packetSize {
		return nil, errors.New("ntp: 报文长度不足 48 字节")
	}
	p := &Packet{}
	p.LI = b[0] >> 6
	p.VN = (b[0] >> 3) & 0x7
	p.Mode = b[0] & 0x7
	p.Stratum = b[1]
	p.Poll = int8(b[2])
	p.Precision = int8(b[3])
	p.RootDelay = binary.BigEndian.Uint32(b[4:8])
	p.RootDispersion = binary.BigEndian.Uint32(b[8:12])
	p.ReferenceID = binary.BigEndian.Uint32(b[12:16])
	p.ReferenceTime = getNTP(b[16:24])
	p.OriginateTime = getNTP(b[24:32])
	p.ReceiveTime = getNTP(b[32:40])
	p.TransmitTime = getNTP(b[40:48])
	return p, nil
}

// ClientOffsetDelay 客户端依据收到的响应与本地 T1/T4 计算偏差与时延。
func (p *Packet) ClientOffsetDelay(t1, t4 time.Time) (offset, delay time.Duration) {
	t2 := p.ReceiveTime.ToTime()
	t3 := p.TransmitTime.ToTime()
	return ComputeOffsetDelay(t1, t2, t3, t4)
}

// ComputeOffsetDelay 按 SNTP 算法由四个时间戳计算时钟偏差与往返时延：
// offset = ((T2-T1)+(T3-T4))/2，delay = (T4-T1)-(T3-T2)。
func ComputeOffsetDelay(t1, t2, t3, t4 time.Time) (offset, delay time.Duration) {
	d1 := t2.Sub(t1)
	d2 := t3.Sub(t4)
	offset = (d1 + d2) / 2
	d3 := t4.Sub(t1)
	d4 := t3.Sub(t2)
	delay = d3 - d4
	return offset, delay
}

func putNTP(b []byte, n NTPTime) {
	binary.BigEndian.PutUint32(b[0:4], n.Seconds)
	binary.BigEndian.PutUint32(b[4:8], n.Fraction)
}

func getNTP(b []byte) NTPTime {
	return NTPTime{
		Seconds:  binary.BigEndian.Uint32(b[0:4]),
		Fraction: binary.BigEndian.Uint32(b[4:8]),
	}
}
