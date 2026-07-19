package monitor

import (
	"testing"
	"time"
)

func TestParsePushTime(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantHH int
		wantMM int
	}{
		{"正常 07:00", "07:00", 7, 0},
		{"正常 23:59", "23:59", 23, 59},
		{"空串回落 07:00", "", 7, 0},
		{"缺分钟回落", "7", 7, 0},
		{"小时越界回落", "25:00", 7, 0},
		{"分钟越界回落", "08:70", 7, 0},
		{"非数字回落", "aa:bb", 7, 0},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			hh, mm := parsePushTime(c.in)
			if hh != c.wantHH || mm != c.wantMM {
				t.Fatalf("parsePushTime(%q) = %d:%d, want %d:%d", c.in, hh, mm, c.wantHH, c.wantMM)
			}
		})
	}
}

func TestNextRun(t *testing.T) {
	loc := time.UTC // 测试用 UTC，避免依赖机器时区
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "当天时刻未到 → 今天",
			now:  time.Date(2026, 7, 19, 6, 0, 0, 0, loc),
			want: time.Date(2026, 7, 19, 7, 0, 0, 0, loc),
		},
		{
			name: "当天时刻已过 → 次日",
			now:  time.Date(2026, 7, 19, 8, 0, 0, 0, loc),
			want: time.Date(2026, 7, 20, 7, 0, 0, 0, loc),
		},
		{
			name: "正好等于该时刻 → 次日（!After 为真）",
			now:  time.Date(2026, 7, 19, 7, 0, 0, 0, loc),
			want: time.Date(2026, 7, 20, 7, 0, 0, 0, loc),
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := nextRun(c.now, 7, 0, loc)
			if !got.Equal(c.want) {
				t.Fatalf("nextRun(%v) = %v, want %v", c.now, got, c.want)
			}
		})
	}
}
