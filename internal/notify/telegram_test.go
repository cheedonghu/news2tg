package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// 使用本地 Telegram API 替身验证实际请求，避免路由正确却丢失 Markdown 格式。
func TestNotifyMarkdownTo(t *testing.T) {
	const content = "*北京*  多云  24℃\\~33℃"
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("chat_id") != "123" || r.Form.Get("text") != content || r.Form.Get("parse_mode") != "MarkdownV2" {
			t.Errorf("unexpected Telegram request: %v", r.Form)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":123,"type":"private"}}}`))
	}))
	defer srv.Close()
	bot := &tgbotapi.BotAPI{Token: "test", Client: srv.Client()}
	bot.SetAPIEndpoint(srv.URL + "/bot%s/%s")
	tg := &Telegram{bot: bot, chatID: -100999}
	if err := tg.NotifyMarkdownTo(context.Background(), 123, content); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("未发送请求")
	}
}
