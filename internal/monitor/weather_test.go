package monitor

import (
	"context"
	"net/http"
	"net/http/httptest"
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
			"每日天气",     // 标题
			"*北京*",     // 城市加粗
			"多云",       // 状况
			`24℃\~33℃`, // 温度：low~high，~ 被转义为 \~
			"*广州*",
			"👕",                         // 穿衣建议段标记
			"北京：加件外套",                   // 建议正文
			"[老王](tg://user?id=234567)", // @ 提及链接
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

func TestFetchCity(t *testing.T) {
	// 模拟中国天气网 cityinfo 接口返回。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/101010100.html") {
			w.Write([]byte(`{"weatherinfo":{"city":"北京","cityid":"101010100","temp1":"33℃","temp2":"24℃","weather":"多云"}}`))
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	// 直接用字面量构造，注入测试 baseURL 与 httptest client。
	wm := &Weather{httpClient: srv.Client(), baseURL: srv.URL}

	t.Run("正常解析", func(t *testing.T) {
		got, err := wm.fetchCity(context.Background(), "101010100")
		if err != nil {
			t.Fatalf("fetchCity 意外报错: %v", err)
		}
		if got.Name != "北京" || got.Weather != "多云" || got.High != "33℃" || got.Low != "24℃" {
			t.Fatalf("解析结果不对: %+v", got)
		}
	})

	t.Run("404 → 报错", func(t *testing.T) {
		if _, err := wm.fetchCity(context.Background(), "999999999"); err == nil {
			t.Fatalf("非 200 响应应报错")
		}
	})
}

// —— 测试用 fake ——

type fakeNotifier struct{ batch []string }                                               // 捕获发出去的消息
func (f *fakeNotifier) Notify(ctx context.Context, content string) error                 { return nil }
func (f *fakeNotifier) NotifyTo(ctx context.Context, chatID int64, content string) error { return nil }
func (f *fakeNotifier) NotifyMarkdown(ctx context.Context, content string) error {
	f.batch = append(f.batch, content)
	return nil
}

type fakeAdvisor struct{ out string } // 固定返回建议文本
func (f fakeAdvisor) Advise(ctx context.Context, weatherText string) (string, error) {
	return f.out, nil
}

func TestPushOnce(t *testing.T) {
	// 服务器：101010100 正常，888888888 返回 500（模拟单城市失败）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/101010100.html") {
			w.Write([]byte(`{"weatherinfo":{"city":"北京","temp1":"33℃","temp2":"24℃","weather":"多云"}}`))
			return
		}
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Run("部分城市失败仍推送好的那个 + 带建议", func(t *testing.T) {
		fn := &fakeNotifier{}
		wm := &Weather{
			httpClient: srv.Client(),
			notifier:   fn,
			advisor:    fakeAdvisor{out: "北京：多穿点"},
			baseURL:    srv.URL,
		}
		cfg := &config.Config{Features: config.Features{
			WeatherCities: []string{"101010100", "888888888"}, // 后者会失败
		}}

		if err := wm.pushOnce(context.Background(), cfg, "2026-07-19"); err != nil {
			t.Fatalf("pushOnce 意外报错: %v", err)
		}
		if len(fn.batch) != 1 {
			t.Fatalf("应恰好推送 1 条，实际 %d 条", len(fn.batch))
		}
		msg := fn.batch[0]
		if !strings.Contains(msg, "*北京*") || !strings.Contains(msg, "北京：多穿点") {
			t.Fatalf("消息内容缺失:\n%s", msg)
		}
		// 锁定修复：日期由调用方传入并原样出现在消息标题里，不再由 pushOnce 内部取 time.Now()。
		// 注意 EscapeMarkdownV2 会把 '-' 转义为 '\-'，标题里实际是 "2026\-07\-19"。
		if !strings.Contains(msg, `2026\-07\-19`) {
			t.Fatalf("消息应包含调用方传入的日期 2026-07-19:\n%s", msg)
		}
	})

	t.Run("全部城市失败 → 不推送", func(t *testing.T) {
		fn := &fakeNotifier{}
		wm := &Weather{
			httpClient: srv.Client(),
			notifier:   fn,
			advisor:    fakeAdvisor{out: "x"},
			baseURL:    srv.URL,
		}
		cfg := &config.Config{Features: config.Features{
			WeatherCities: []string{"888888888"}, // 唯一城市失败
		}}

		if err := wm.pushOnce(context.Background(), cfg, "2026-07-19"); err != nil {
			t.Fatalf("pushOnce 意外报错: %v", err)
		}
		if len(fn.batch) != 0 {
			t.Fatalf("全失败时不应推送，实际推了 %d 条", len(fn.batch))
		}
	})
}
