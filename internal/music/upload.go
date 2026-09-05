package music

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/cheedonghu/news2tg/internal/tools"
)

// targetUploadTimeout 是**每个**目标的上传超时。
// alist 转发到网盘可能很慢，给宽松一点；两个目标是并发跑的，不会叠加。
const targetUploadTimeout = 300 * time.Second

// maxFilenameRunes 是文件名主体（不含 .mp3）的最大字符数。
// 按 rune 而不是字节算：多数文件系统和网盘的限制是 255 字节，
// 中文一个字 3 字节，120 个字符最多 360 字节 —— 还是可能超，
// 但配合各家网盘普遍更宽松的实际限制，这个值足够安全且不至于砍掉正常歌名。
const maxFilenameRunes = 120

// Target 是一个 WebDAV 上传目标。
type Target struct {
	Name string // 展示名，如 "阿里云盘"，只用于进度消息
	URL  string // 目录地址，如 http://alist:5244/dav/aliyun/Music
}

// UploadFile 是一次任务里要传给每个目标的一个文件。
type UploadFile struct {
	LocalPath   string // 本地路径
	Filename    string // 传到目标上的文件名（会被 url.PathEscape 转义）
	ContentType string // 如 "audio/mpeg" / "text/plain; charset=utf-8"
	// Optional 为 true 表示这个文件传失败**不算目标失败**。
	// 歌词就是这样的附赠品：不能因为一个 .lrc 没传上去，就把已经传好的
	// 歌判成失败 —— 那和"不为一个网盘挂掉丢弃已下好的歌"是同一条约定。
	Optional bool
}

// Uploader 把本地文件并发 PUT 到所有目标。
//
// 注意：内部是按 []Target 循环的，不硬编码"两个"。
// 配置层之所以是六个平铺字段，只是因为需求明确说了不做动态列表；
// 这里保持切片形态，加第三个网盘时这个文件一行都不用改。
type Uploader struct {
	httpClient *http.Client
	targets    []Target
	user       string
	pass       string
	timeout    time.Duration // 抽成字段而非直接用常量，方便测试压短
}

// NewUploader 构造函数。user/pass 是所有目标共用的 WebDAV 凭据
// （alist 单实例挂多个网盘，账号是同一个）。
func NewUploader(httpClient *http.Client, targets []Target, user, pass string) *Uploader {
	return &Uploader{
		httpClient: httpClient,
		targets:    targets,
		user:       user,
		pass:       pass,
		timeout:    targetUploadTimeout,
	}
}

// Upload 并发把 files 传到所有目标。
//
// files 的顺序有意义：每个目标**顺序**传自己那份，排在前面的先落地。
// 调用方应把要紧的文件放在前面（mp3 在前、歌词在后）——ctx 中途断掉时，
// 先传完的是要紧那个。
//
// onProgress 在初始时以及**每个目标完成时**各回调一次，参数是当前所有目标
// 状态的快照副本（不是内部切片本身，调用方随便读不用担心竞态）。可传 nil。
//
// 返回值：最终状态切片总是返回（哪怕全失败，调用方要靠它渲染每个目标的成败）；
// error 仅在**所有**目标都失败时才非 nil —— 至少一个成功就算任务成功，
// 延续仓库"AI/digest 失败不阻断推送"的既有约定，不为一个网盘挂掉丢弃已下好的歌。
// 注意"目标失败"只由**必传**文件决定：可选文件失败只记进 TargetStatus.Warn。
func (u *Uploader) Upload(ctx context.Context, files []UploadFile, onProgress func([]TargetStatus)) ([]TargetStatus, error) {
	states := make([]TargetStatus, len(u.targets))
	for i, t := range u.targets {
		// 初始状态是"排队中"而不是"上传中"：这一刻一个 goroutine 都还没起，
		// 直接写 TargetRunning 是在撒谎（顺带也让 TargetPending 变成了死代码，
		// renderStatus 里的 ⬜ 分支永远走不到）。
		// 真正翻成 TargetRunning 的时机在下面每个 goroutine 的入口处。
		states[i] = TargetStatus{Name: t.Name, State: TargetPending}
	}

	// mu 保护 states：多个上传 goroutine 会并发改各自那一格。
	var mu sync.Mutex
	// notify 拷一份快照再回调，避免调用方拿着内部切片在锁外读，产生数据竞态。
	notify := func() {
		mu.Lock()
		// append(nil, s...) 是 Go 里拷贝切片的惯用写法。
		snapshot := append([]TargetStatus(nil), states...)
		mu.Unlock()
		if onProgress != nil {
			onProgress(snapshot)
		}
	}
	notify() // 初始快照：让进度消息立刻显示出"上传中"的骨架

	// sync.WaitGroup 是计数器：Add(n) 加，Done() 减 1，Wait() 阻塞到归零。
	var wg sync.WaitGroup
	for i, t := range u.targets {
		wg.Add(1)
		// Go 1.22 起 for-range 变量每轮都是新变量，不必再手动 shadow；
		// 但显式传参更直白，也和仓库里其它并发代码的谨慎风格一致。
		go func(idx int, tg Target) {
			defer wg.Done()

			// 进到这里才算真的开始传，此时才翻成"上传中"。
			// 刻意**不**在这里额外 notify 一次：N 个目标就会多出 N 次进度刷新，
			// 而它们几乎同时发生，节流窗口里最终也只合并成一帧，白占发送锁。
			// 这个状态会搭下一次 notify（某个目标传完时）的顺风车一起发出去。
			mu.Lock()
			states[idx].State = TargetRunning
			mu.Unlock()

			// 每个目标独立超时：一个网盘卡住不该拖垮另一个。
			cctx, cancel := context.WithTimeout(ctx, u.timeout)
			defer cancel()

			warn, err := u.putAll(cctx, tg, files)

			mu.Lock()
			states[idx].Warn = warn
			if err != nil {
				states[idx].State = TargetFailed
				states[idx].Err = err.Error()
			} else {
				states[idx].State = TargetOK
			}
			mu.Unlock()

			if err != nil {
				slog.ErrorContext(ctx, "WebDAV 上传失败", "target", tg.Name, "err", err)
			} else {
				slog.InfoContext(ctx, "WebDAV 上传成功", "target", tg.Name, "files", len(files), "warn", warn)
			}
			notify()
		}(i, t)
	}
	wg.Wait()

	mu.Lock()
	final := append([]TargetStatus(nil), states...)
	mu.Unlock()

	okCount := 0
	for _, s := range final {
		if s.State == TargetOK {
			okCount++
		}
	}
	if okCount == 0 {
		return final, fmt.Errorf("全部 %d 个 WebDAV 目标上传失败", len(final))
	}
	return final, nil
}

// putAll 把 files 依次传到一个目标。
//
// 返回 (可选文件的失败说明, 必传文件的失败)。error 非 nil 表示这个目标失败了。
//
// 为什么顺序传而不是并发：同一个目标就那么点带宽，并发只会互相抢；而且
// 顺序执行才能兑现"必传文件失败后不再传后面的"——那个目标已经废了，
// 继续传歌词是白占一次请求。
func (u *Uploader) putAll(ctx context.Context, t Target, files []UploadFile) (string, error) {
	var warn string
	for _, f := range files {
		err := u.putOne(ctx, t, f)
		if err == nil {
			continue
		}
		if !f.Optional {
			// 必传文件失败：这个目标已经废了，不必再传后面的。
			return warn, err
		}
		// 可选文件失败：目标仍算成功，只留一句说明。
		// 多个可选文件都失败时后一条覆盖前一条 —— 目前只有一个可选文件
		// （歌词），真出现多个再改成拼接也不迟。
		slog.ErrorContext(ctx, "可选文件上传失败", "target", t.Name, "file", f.Filename, "err", err)
		warn = f.Filename + ": " + err.Error()
	}
	return warn, nil
}

// putOne 把单个文件 PUT 到单个目标。
func (u *Uploader) putOne(ctx context.Context, t Target, uf UploadFile) error {
	// 每个文件各自打开一次：*os.File 内部有共享的读偏移量，
	// 多个 goroutine 拿同一个 *os.File 当 body 会互相把对方的读位置搅乱。
	f, err := os.Open(uf.LocalPath)
	if err != nil {
		return fmt.Errorf("打开本地文件失败: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("读取本地文件信息失败: %w", err)
	}

	// url.PathEscape 转义文件名里的空格、中文等，避免拼出非法 URL。
	// TrimRight 去掉配置里可能多写的尾斜杠，防止出现 //。
	dst := strings.TrimRight(t.URL, "/") + "/" + url.PathEscape(uf.Filename)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, dst, f)
	if err != nil {
		return err
	}
	// 显式给出长度：不设的话 net/http 会走 chunked 传输，
	// 部分 WebDAV 服务端（含某些 alist 后端）会直接拒绝。
	req.ContentLength = fi.Size()
	req.SetBasicAuth(u.user, u.pass)
	// 每个文件用自己的类型：歌词是文本，声明成 audio/mpeg 会让某些
	// WebDAV 后端存成二进制、下载回来变成乱码。
	req.Header.Set("Content-Type", uf.ContentType)

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV PUT 请求失败: %w", err)
	}
	defer resp.Body.Close()
	// 把响应体读干净才能让连接回到连接池复用；PUT 的响应体通常是空的，丢掉即可。
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV 返回 %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

// filenameReplacer 把文件系统 / URL 路径里的危险字符换掉。
// 换行单独换成空格而不是下划线：它多半来自模型输出的意外折行，换空格更自然。
var filenameReplacer = strings.NewReplacer(
	"/", "_",
	`\`, "_",
	":", "_",
	"*", "_",
	"?", "_",
	`"`, "_",
	"<", "_",
	">", "_",
	"|", "_",
	"\n", " ",
	"\r", " ",
)

// buildBase 拼出文件名主体（不含扩展名）：清洗危险字符、兜底、按 rune 截断。
//
// 抽出来是为了 .mp3 与 .lrc 共用**同一个**主体。两个文件名必须逐字一致
// （含截断结果），播放器才能靠"同目录同名"把歌词配给音频；差一个字就永远
// 配不上，而且不会有任何报错。这个函数就是那条约束唯一的保障点。
//
// 为什么必须清洗：歌名里的 '/' 会被 WebDAV 当成路径分隔符，把文件写到一个
// 意料之外的子目录里（甚至 404）。其余字符是 Windows 文件名非法字符，
// 网盘客户端同步下来会出问题。
func buildBase(artist, title string) string {
	a := strings.TrimSpace(filenameReplacer.Replace(artist))
	tt := strings.TrimSpace(filenameReplacer.Replace(title))
	base := strings.TrimSpace(a + " - " + tt)

	// 歌手和歌名都空时会剩下一个孤零零的 "-"，兜个底避免出现 " - .mp3"。
	if strings.Trim(base, " -") == "" {
		base = "unknown"
	}
	// TruncateUTF8 按 rune 截断，绝不会把一个中文字符切成两半。
	return tools.TruncateUTF8(base, maxFilenameRunes)
}

// BuildFilename 拼出 "<歌手> - <歌名>.mp3"。
func BuildFilename(artist, title string) string {
	return buildBase(artist, title) + ".mp3"
}

// BuildLyricFilename 拼出 "<歌手> - <歌名>.lrc"，与 BuildFilename 同主体。
func BuildLyricFilename(artist, title string) string {
	return buildBase(artist, title) + ".lrc"
}
