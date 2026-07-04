package command

import (
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// cmdMsg 造一条"命令消息"：Text + 一个起始于 0 的 bot_command 实体，
// 这样 SDK 的 IsCommand()/Command()/CommandArguments() 才能正确解析。
// cmdLen 是命令本身的长度（含 /），如 "/summary" = 8。
func cmdMsg(text string, cmdLen int, fromID int64) *tgbotapi.Message {
	return &tgbotapi.Message{
		Text:     text,
		Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: cmdLen}},
		From:     &tgbotapi.User{ID: fromID},
		Chat:     &tgbotapi.Chat{ID: 999},
	}
}

func TestClassify(t *testing.T) {
	// 白名单只含 111。
	b := &Bot{admins: map[int64]bool{111: true}}

	const sumLen = len("/summary") // 8

	cases := []struct {
		name    string
		msg     *tgbotapi.Message
		wantAct action
		wantURL string
	}{
		{
			name:    "nil 消息 → 忽略",
			msg:     nil,
			wantAct: actionIgnore,
		},
		{
			name:    "非命令文本 → 忽略",
			msg:     &tgbotapi.Message{Text: "https://example.com", From: &tgbotapi.User{ID: 111}, Chat: &tgbotapi.Chat{ID: 1}},
			wantAct: actionIgnore,
		},
		{
			name:    "别的命令 → 忽略",
			msg:     cmdMsg("/start", len("/start"), 111),
			wantAct: actionIgnore,
		},
		{
			name:    "非白名单发 /summary → 未授权",
			msg:     cmdMsg("/summary https://example.com", sumLen, 222),
			wantAct: actionUnauthorized,
		},
		{
			name:    "白名单但无参数 → 提示用法",
			msg:     cmdMsg("/summary", sumLen, 111),
			wantAct: actionUsage,
		},
		{
			name:    "白名单但参数非 http → 提示用法",
			msg:     cmdMsg("/summary 随便写点啥", sumLen, 111),
			wantAct: actionUsage,
		},
		{
			name:    "白名单 + 合法网址 → 去总结",
			msg:     cmdMsg("/summary https://example.com/x", sumLen, 111),
			wantAct: actionSummarize,
			wantURL: "https://example.com/x",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			act, url := b.classify(c.msg)
			if act != c.wantAct {
				t.Fatalf("act = %d, want %d", act, c.wantAct)
			}
			if url != c.wantURL {
				t.Errorf("url = %q, want %q", url, c.wantURL)
			}
		})
	}
}
