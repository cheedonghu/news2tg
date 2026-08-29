package music

import "errors"

// errLyricUnsupported 是"该音源不提供歌词"的哨兵错误，供 errors.Is 判别。
//
// 为什么"不供词"要用一个专门的错误、而不是跟"站点没收录这首"一样返回
// ("", nil)：这两件事在进度消息里是不同的两句话 ——「mp3pm 不提供」
// 与「站点未收录」。合并成一句含糊的"无歌词"，代价是日后 musicso 改版
// 导致歌词永远解析不出来时，界面上跟"这首歌真的没词"长得一模一样，
// 没有人会发现有东西坏了。
//
// 风格与 agent.go 的 errTooLarge / errSourceUnavailable 一致：
// 音源可以用 %w 包任意层数，调用方一律用 errors.Is 判别。
var errLyricUnsupported = errors.New("该音源不提供歌词")
