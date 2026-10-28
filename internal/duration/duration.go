// Package duration 提供宽松的时间长度解析：兼容 Go 标准 time.ParseDuration
// 与中文单位（分/秒/小时/天），并支持纯数字默认按「分」解析。
//
// 设计目标：
//   - 保持对既有英文格式（15m、5s、1h30m）的完全兼容；
//   - 允许用户用中文单位输入（如「15分」「5秒」「1小时30分」）；
//   - 纯数字（如「15」）按「分」解析，契合「校时间隔以分为单位」的使用习惯。
package duration

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse 解析时间长度字符串为 time.Duration。
//
// 解析顺序：
//  1. 先尝试 Go 标准 time.ParseDuration（支持 15m / 5s / 1h30m 等）；
//  2. 失败后尝试中文单位解析（支持 分/分钟、秒/秒钟、时/小时、天）；
//  3. 纯数字按「分」解析（如 "15" → 15 分钟）。
//
// 支持的格式示例：
//
//	"15m"、"5s"、"1h30m"、"90s"        → 标准格式
//	"15分"、"15分钟"、"5秒"、"90秒钟"    → 中文单位
//	"1小时30分"、"1天"                  → 中文组合
//	"15"                               → 15 分钟（纯数字默认按分）
func Parse(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("时间长度不能为空")
	}

	// 1. 标准 Go 格式（含负号、小数）。
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}

	// 2. 纯数字 → 按「分」解析。
	if isAllDigits(s) {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析时间长度 %q", s)
		}
		return time.Duration(n * float64(time.Minute)), nil
	}

	// 3. 中文单位解析。
	return parseChinese(s)
}

// chineseUnit 描述一个中文单位的匹配正则与对应时长。
type chineseUnit struct {
	re   *regexp.Regexp
	unit time.Duration
}

var chineseUnits = []chineseUnit{
	{regexp.MustCompile(`天`), 24 * time.Hour},
	{regexp.MustCompile(`小时|时`), time.Hour},
	{regexp.MustCompile(`分钟|分`), time.Minute},
	{regexp.MustCompile(`秒钟|秒`), time.Second},
}

// numRe 匹配一个带符号/小数的数字。
var numRe = regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)

// parseChinese 解析含中文单位的时间长度，如 "1小时30分"、"90秒钟"、"15分"。
func parseChinese(s string) (time.Duration, error) {
	// 逐段扫描：找到「数字+单位」对，累加时长；期间若出现无法识别的字符则报错。
	rest := s
	var total time.Duration
	matchedAny := false

	for len(rest) > 0 {
		// 定位下一个数字。
		loc := numRe.FindStringIndex(rest)
		if loc == nil {
			return 0, fmt.Errorf("无法解析时间长度 %q（缺少数字）", s)
		}
		// 数字之前若存在非空白字符，说明格式非法。
		if strings.TrimSpace(rest[:loc[0]]) != "" {
			return 0, fmt.Errorf("无法解析时间长度 %q（存在无法识别的字符）", s)
		}

		numStr := rest[loc[0]:loc[1]]
		n, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("无法解析数字 %q", numStr)
		}

		// 数字之后必须紧跟单位。
		after := rest[loc[1]:]
		var unit time.Duration
		var consumed int
		for _, u := range chineseUnits {
			if idx := u.re.FindStringIndex(after); idx != nil && idx[0] == 0 {
				unit = u.unit
				consumed = idx[1]
				break
			}
		}
		if consumed == 0 {
			return 0, fmt.Errorf("无法解析时间长度 %q（数字 %s 后缺少单位）", s, numStr)
		}

		total += time.Duration(n * float64(unit))
		matchedAny = true
		rest = after[consumed:]
	}

	if !matchedAny {
		return 0, fmt.Errorf("无法解析时间长度 %q", s)
	}
	return total, nil
}

// isAllDigits 判断字符串是否为纯数字（可含小数点、正负号）。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	// 允许一个可选符号 + 数字（可带一个小数点）。
	seenDot := false
	for i, r := range s {
		switch {
		case r == '+' || r == '-':
			if i != 0 {
				return false
			}
		case r == '.':
			if seenDot {
				return false
			}
			seenDot = true
		case r >= '0' && r <= '9':
			// ok
		default:
			return false
		}
	}
	return true
}

// Format 将时长格式化为「中文单位」友好字符串（用于界面展示）。
// 例如 15m → "15分钟"，5s → "5秒"，1h30m → "1小时30分钟"。
func Format(d time.Duration) string {
	if d == 0 {
		return "0秒"
	}
	neg := d < 0
	if neg {
		d = -d
	}

	days := int64(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int64(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int64(d / time.Minute)
	d -= time.Duration(mins) * time.Minute
	secs := float64(d) / float64(time.Second)

	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d天", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
	}
	if mins > 0 {
		parts = append(parts, fmt.Sprintf("%d分钟", mins))
	}
	if secs > 0 || len(parts) == 0 {
		// 秒可能带小数。
		if secs == float64(int64(secs)) {
			parts = append(parts, fmt.Sprintf("%d秒", int64(secs)))
		} else {
			parts = append(parts, fmt.Sprintf("%.3g秒", secs))
		}
	}

	out := strings.Join(parts, "")
	if neg {
		out = "-" + out
	}
	return out
}
