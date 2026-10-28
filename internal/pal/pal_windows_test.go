//go:build windows

package pal

import (
	"testing"
	"time"
)

// TestSystemTimeFromUsesUTC 是「时钟被整体拨快一个时区」这个 bug 的回归测试。
//
// 背景：kernel32 的 SetSystemTime 按 UTC 解释入参，早期实现误用了 t.Local() 的
// 分量，在 UTC+8 下每设一次钟就把系统时间拨快 8 小时，且下一轮测量会得到 -8h 的
// 偏差、再设一次又加 8h，形成永久偏移。本测试只校验换算，不触碰系统时钟。
func TestSystemTimeFromUsesUTC(t *testing.T) {
	// 同一时刻的两种表述：本地 21:31:22.500 (+0800) == UTC 13:31:22.500。
	cst := time.FixedZone("CST", 8*3600)
	local := time.Date(2026, 10, 28, 21, 31, 22, 500*int(time.Millisecond), cst)
	utc := local.UTC()
	if !local.Equal(utc) {
		t.Fatal("测试前提错误：两者应是同一时刻")
	}

	st := systemTimeFrom(local)
	if st.Hour != 13 || st.Minute != 31 || st.Second != 22 {
		t.Fatalf("传入本地时刻应换算为 UTC 13:31:22，实际 %02d:%02d:%02d"+
			"（若得到 21:31:22 说明又把本地分量当 UTC，系统时间会被拨快一个时区）",
			st.Hour, st.Minute, st.Second)
	}
	if st.Milliseconds != 500 {
		t.Fatalf("毫秒应为 500，实际 %d", st.Milliseconds)
	}
	if st.Year != 2026 || int(st.Month) != 10 || st.Day != 28 {
		t.Fatalf("日期应为 2026-10-28，实际 %d-%d-%d", st.Year, st.Month, st.Day)
	}

	// 同一时刻换一种时区表述，结果必须完全一致（换算不该依赖入参所在时区）。
	stUTC := systemTimeFrom(utc)
	if stUTC != st {
		t.Fatalf("同一时刻的 UTC/本地表述应得到相同 SYSTEMTIME，实际 %+v vs %+v", stUTC, st)
	}

	// 再验一个跨零点的例子：本地 2026-10-29 03:00 (+0800) == UTC 2026-10-28 19:00。
	next := time.Date(2026, 10, 29, 3, 0, 0, 0, cst)
	stNext := systemTimeFrom(next)
	if stNext.Day != 28 || stNext.Hour != 19 {
		t.Fatalf("UTC+8 的次日 03:00 应换算为前一日 19:00 UTC，实际 %d 日 %02d 时",
			stNext.Day, stNext.Hour)
	}
}
