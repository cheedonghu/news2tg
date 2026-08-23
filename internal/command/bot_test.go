package command

import (
	"context"
	"strings"
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

// fakeMusic 是 MusicRunner 的空实现，只为让 classify 能看到"音乐功能已配置"。
type fakeMusic struct{}

func (fakeMusic) Run(_ context.Context, _ int64, _ string) error { return nil }

func TestClassify(t *testing.T) {
	// 白名单只含 111；音乐功能已配置。
	b := &Bot{admins: map[int64]bool{111: true}, music: fakeMusic{}}

	const (
		sumLen   = len("/summary") // 8
		musicLen = len("/music")   // 6
	)

	cases := []struct {
		name     string
		msg      *tgbotapi.Message
		wantAct  action
		wantCmd  string
		wantArgs string
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
			wantCmd: commandSummary,
		},
		{
			name:    "白名单但无参数 → 提示用法",
			msg:     cmdMsg("/summary", sumLen, 111),
			wantAct: actionUsage,
			wantCmd: commandSummary,
		},
		{
			name:    "白名单但参数非 http → 提示用法",
			msg:     cmdMsg("/summary 随便写点啥", sumLen, 111),
			wantAct: actionUsage,
			wantCmd: commandSummary,
		},
		{
			name:     "白名单 + 合法网址 → 去执行",
			msg:      cmdMsg("/summary https://example.com/x", sumLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandSummary,
			wantArgs: "https://example.com/x",
		},
		{
			name:    "非白名单发 /music → 未授权",
			msg:     cmdMsg("/music 晴天", musicLen, 222),
			wantAct: actionUnauthorized,
			wantCmd: commandMusic,
		},
		{
			name:    "白名单 /music 但无参数 → 提示用法",
			msg:     cmdMsg("/music", musicLen, 111),
			wantAct: actionUsage,
			wantCmd: commandMusic,
		},
		{
			name:     "白名单 /music + 自由格式歌名 → 去执行",
			msg:      cmdMsg("/music 晴天 - 周杰伦", musicLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandMusic,
			wantArgs: "晴天 - 周杰伦",
		},
		{
			name:     "/music 参数不需要是网址，随便什么格式都收",
			msg:      cmdMsg("/music 周杰伦那首晴天", musicLen, 111),
			wantAct:  actionRun,
			wantCmd:  commandMusic,
			wantArgs: "周杰伦那首晴天",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := b.classify(c.msg)
			if got.act != c.wantAct {
				t.Fatalf("act = %d, want %d", got.act, c.wantAct)
			}
			if got.cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", got.cmd, c.wantCmd)
			}
			if got.args != c.wantArgs {
				t.Errorf("args = %q, want %q", got.args, c.wantArgs)
			}
		})
	}
}

// TestClassifyMusicUnavailable 验证：[music] 未配置（music 依赖为 nil）时，
// 白名单用户发 /music 得到"未配置"而不是被静默忽略 —— 否则用户会以为 bot 坏了。
func TestClassifyMusicUnavailable(t *testing.T) {
	b := &Bot{admins: map[int64]bool{111: true}} // music 字段留 nil

	got := b.classify(cmdMsg("/music 晴天", len("/music"), 111))
	if got.act != actionUnavailable {
		t.Fatalf("act = %d, want actionUnavailable(%d)", got.act, actionUnavailable)
	}
	if got.cmd != commandMusic {
		t.Errorf("cmd = %q, want %q", got.cmd, commandMusic)
	}
}

// TestUsageText 验证两条指令的用法提示各说各的，不会串。
func TestUsageText(t *testing.T) {
	if !strings.Contains(usageText(commandSummary), "/summary") {
		t.Errorf("summary 用法提示不对: %q", usageText(commandSummary))
	}
	if !strings.Contains(usageText(commandMusic), "/music") {
		t.Errorf("music 用法提示不对: %q", usageText(commandMusic))
	}
}
