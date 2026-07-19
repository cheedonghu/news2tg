package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/cheedonghu/news2tg/internal/config"
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

func TestBuildMessage(t *testing.T) {
	items := []cityWeather{
		{Name: "北京", Weather: "多云", High: "33℃", Low: "24℃"},
		{Name: "广州", Weather: "雷阵雨", High: "34℃", Low: "27℃"},
	}
	mentions := []config.Mention{{ID: 234567, Name: "老王"}}

	t.Run("完整：城市/建议/提及都在", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "北京：加件外套", mentions)
		wants := []string{
			"每日天气",                          // 标题
			"*北京*",                          // 城市加粗
			"多云",                            // 状况
			`24℃\~33℃`,                     // 温度：low~high，~ 被转义为 \~
			"*广州*",
			"👕",                            // 穿衣建议段标记
			"北京：加件外套",                       // 建议正文
			"[老王](tg://user?id=234567)",    // @ 提及链接
		}
		for _, w := range wants {
			if !strings.Contains(msg, w) {
				t.Fatalf("消息缺少 %q\n完整消息:\n%s", w, msg)
			}
		}
	})

	t.Run("无建议：省略穿衣建议段", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "", mentions)
		if strings.Contains(msg, "👕") {
			t.Fatalf("advice 为空时不应出现穿衣建议段:\n%s", msg)
		}
	})

	t.Run("无提及：省略提醒段", func(t *testing.T) {
		msg := buildMessage("2026-07-19", items, "建议", nil)
		if strings.Contains(msg, "tg://user?id=") {
			t.Fatalf("mentions 为空时不应出现提及链接:\n%s", msg)
		}
	})
}
