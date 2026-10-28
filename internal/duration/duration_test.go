package duration

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		// 标准 Go 格式（兼容性）
		{"15m", 15 * time.Minute},
		{"5s", 5 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"90s", 90 * time.Second},
		{"1.5h", 90 * time.Minute},

		// 纯数字 → 按分
		{"15", 15 * time.Minute},
		{"0", 0},
		{"1.5", 90 * time.Second},

		// 中文单位
		{"15分", 15 * time.Minute},
		{"15分钟", 15 * time.Minute},
		{"5秒", 5 * time.Second},
		{"90秒钟", 90 * time.Second},
		{"1小时", 1 * time.Hour},
		{"1时", 1 * time.Hour},
		{"1天", 24 * time.Hour},
		{"1小时30分", 90 * time.Minute},
		{"2小时15分钟30秒", 2*time.Hour + 15*time.Minute + 30*time.Second},
	}

	for _, c := range cases {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("Parse(%q) 出错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Parse(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{"", "abc", "分", "15xx", "m15", "15分x"}
	for _, s := range bad {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) 应报错，实际通过", s)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{15 * time.Minute, "15分钟"},
		{5 * time.Second, "5秒"},
		{90 * time.Minute, "1小时30分钟"},
		{24 * time.Hour, "1天"},
		{0, "0秒"},
		{2*time.Hour + 15*time.Minute + 30*time.Second, "2小时15分钟30秒"},
	}
	for _, c := range cases {
		if got := Format(c.in); got != c.want {
			t.Errorf("Format(%v) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}
