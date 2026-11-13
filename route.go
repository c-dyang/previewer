package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Kind 是内容的分类。它只回答"这是什么"，不回答"谁来画"——后者是 Renderer。
type Kind string

const (
	KindMarkdown Kind = "markdown"
	KindHTML     Kind = "html"
	KindPDF      Kind = "pdf"
	KindImage    Kind = "image"
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindText     Kind = "text"
	KindBinary   Kind = "binary"
	KindEmpty    Kind = "empty" // 裸开，还没有目标文件，等拖进来
)

// Renderer 是"谁把像素画出来"，是路由的第二维。
// 降级链就是 Renderer 的迁移：RMedia → RExternal，RShell → RDirect，等等。
type Renderer string

const (
	RShell    Renderer = "shell"    // 本进程生成 HTML，WebView2 负责渲染
	RDirect   Renderer = "direct"   // 把 file:// URL 直接交给 Chromium 自己渲染
	RMedia    Renderer = "media"    // <video>/<audio>，走 Chromium 原生解码器
	RExternal Renderer = "external" // 本进程画不了，交给外部程序
)

// 每一条降级都必须带一个可枚举的原因码，禁止静默降级。
const (
	DegNone          = ""
	DegContainer     = "container_unsupported_by_chromium"
	DegCodec         = "codec_unsupported_by_chromium"
	DegImageFormat   = "image_format_unsupported_by_chromium"
	DegTextTruncated = "text_truncated_at_limit"
	DegNoExternal    = "no_external_player_found"
	DegEmptyFile     = "empty_file"
	DegBinaryUnknown = "binary_unidentified"
	DegCodecUnknown  = "codec_unverified"
	DegDropNoPath    = "drop_without_path" // 拖到画面里：拿不到路径，只能就地渲染
)

// dropTitle 是裸开窗口的标题，也是"还没有目标"的标记。
const dropTitle = "previewer"

// Plan 是一次查看的全部决定，probe 直接把它序列化出去。
type Plan struct {
	Path      string   `json:"path"`
	Name      string   `json:"name"`
	Size      int64    `json:"size"`
	Ext       string   `json:"ext"`
	Kind      Kind     `json:"kind"`
	Renderer  Renderer `json:"renderer"`
	Container string   `json:"container,omitempty"`
	Codec     string   `json:"codec,omitempty"`
	Backend   string   `json:"backend,omitempty"`
	Degraded  string   `json:"degraded_reason"`
	Notes     []string `json:"notes,omitempty"`
	Error     string   `json:"error,omitempty"`

	// 渲染素材，不进 JSON。
	Text      string `json:"-"`
	Lines     int    `json:"-"`
	Truncated bool   `json:"-"`
	BlobURL   string `json:"-"` // 拖拽就地渲染时 JS 给的 blob: URL，取代 file://
}

const textCap = 4 << 20 // 4 MiB：纯文本直读上限，超了截断并报 reason

var (
	extMarkdown = set(".md", ".markdown", ".mdown", ".mkd", ".mkdn")
	extHTML     = set(".html", ".htm", ".xhtml", ".shtml")
	extPDF      = set(".pdf")

	// Chromium 认的图片格式（WebView2 157 实测口径）
	extImageOK = set(".png", ".jpg", ".jpeg", ".jfif", ".gif", ".webp", ".bmp", ".ico",
		".svg", ".avif", ".apng")
	// Chromium 不认，需要换解码器（v1 没有自带解码器 ⇒ 换后端）
	extImageBad = set(".tif", ".tiff", ".heic", ".heif", ".psd", ".dds", ".exr",
		".cr2", ".cr3", ".nef", ".arw", ".dng", ".raw", ".hdr", ".tga", ".jxl")

	// WebView2 容器层面能挂 <video> 的
	extVideoWeb = set(".mp4", ".m4v", ".mov", ".webm", ".ogv", ".ogg")
	// 容器层面就不行，必须换播放引擎
	extVideoOther = set(".mkv", ".mk3d", ".avi", ".ts", ".m2ts", ".mts", ".flv",
		".wmv", ".asf", ".rmvb", ".rm", ".mpg", ".mpeg", ".vob", ".3gp", ".divx", ".xvid", ".mxf")

	// Chromium 原生能解的音频
	extAudioWeb = set(".mp3", ".m4a", ".m4b", ".aac", ".wav", ".flac", ".ogg", ".oga", ".opus", ".weba")
	// 解不了（ape/wma/dsd 之类）
	extAudioOther = set(".ape", ".wma", ".aiff", ".aif", ".dsf", ".dff", ".mka", ".amr", ".ac3", ".dts", ".tta", ".wv")

	extText = set(".txt", ".log", ".nfo", ".csv", ".tsv", ".json", ".jsonl", ".xml", ".yaml", ".yml",
		".toml", ".ini", ".inf", ".cfg", ".conf", ".properties", ".reg", ".bat", ".cmd", ".ps1", ".psm1",
		".py", ".go", ".c", ".h", ".cc", ".cpp", ".hpp", ".cs", ".java", ".kt", ".js", ".mjs", ".ts", ".tsx", ".jsx",
		".css", ".scss", ".less", ".sql", ".sh", ".bash", ".zsh", ".rs", ".php", ".rb", ".lua", ".pl", ".r",
		".vue", ".svelte", ".diff", ".patch", ".srt", ".ass", ".vtt", ".lrc", ".tex", ".cmake", ".mk",
		".ahk", ".vbs", ".awk", ".asm", ".s", ".dts", ".ld", ".map", ".gitignore", ".gitattributes",
		".editorconfig", ".dockerignore", ".sum", ".mod", ".lock", ".plist", ".manifest", ".service")
)

// chromiumCodec 是 WebView2 原生解码器支持的视频编码四字码。
// 注意 hvc1/hev1 = HEVC，Chromium 只在装了 HEVC Video Extensions 的机器上才解。
var chromiumCodec = map[string]bool{
	"avc1": true, "avc3": true, // H.264
	"vp09": true, // VP9
	"av01": true, // AV1
	"mp4v": false, "hvc1": false, "hev1": false, "dvhe": false, "dvh1": false, "vp08": false,
}

// chromiumCodecName 把四字码翻成人话，给降级说明用。
var chromiumCodecName = map[string]string{
	"avc1": "H.264", "avc3": "H.264", "vp09": "VP9", "av01": "AV1",
	"hvc1": "HEVC", "hev1": "HEVC", "dvhe": "Dolby Vision", "dvh1": "Dolby Vision",
	"mp4v": "MPEG-4 Part 2", "vp08": "VP8",
}

// EmptyPlan 是裸开（不带文件启动）时的目标：一个空壳窗口，等文件拖进来。
func EmptyPlan() *Plan {
	return &Plan{
		Name:     dropTitle,
		Kind:     KindEmpty,
		Renderer: RShell,
		Degraded: DegNone,
	}
}

// ResolveName 是"只有名字没有路径"时的路由 —— 拖到画面里的那条路。
//
// 为什么不能复用 Resolve：Resolve 要 stat、要读魔数、要扫编码，全都需要文件句柄。
// 而 Chromium 的 drop 事件只给 File 对象，不给路径（2026-11-13 实测 types=["Files"]、
// path 为空、连 text/uri-list 都没有），所以只能按扩展名判类型，内容由 JS 喂过来。
//
// 没有路径 ⇒ 一律带 drop_without_path 降级原因，并且不许走 external（没法起播放器）。
func ResolveName(name string, size int64, b *Backends) *Plan {
	p := &Plan{
		Name:     filepath.Base(name),
		Ext:      strings.ToLower(filepath.Ext(name)),
		Size:     size,
		Degraded: DegDropNoPath,
	}
	switch {
	case extMarkdown[p.Ext]:
		p.Kind, p.Renderer = KindMarkdown, RShell
	case extText[p.Ext] || p.Ext == "":
		p.Kind, p.Renderer = KindText, RShell
	case extHTML[p.Ext]:
		p.Kind, p.Renderer = KindHTML, RShell // blob: 直接塞 iframe
	case extPDF[p.Ext]:
		p.Kind, p.Renderer = KindPDF, RShell
	case extImageOK[p.Ext]:
		p.Kind, p.Renderer = KindImage, RShell
	case extVideoWeb[p.Ext]:
		p.Kind, p.Renderer = KindVideo, RMedia
		p.Container = strings.TrimPrefix(p.Ext, ".")
		p.Notes = append(p.Notes, "没有路径，编码没法嗅探；解不了时窗口里会提示")
	default:
		// 图片/视频/音频里 Chromium 解不了的那一撮，以及一切没号码的：
		// 没有路径就没法换后端，落成二进制/提示页
		p.Kind, p.Renderer = KindBinary, RShell
		p.Notes = append(p.Notes, "这个类型要换后端才能看，但拖到画面里拿不到路径 —— 拖到标题栏或 Ctrl+O")
	}
	_ = b
	return p
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// Resolve 是唯一的判定入口：f(类型, 容器, 编码, 可用后端) —— 绝不是 f(类型)。
func Resolve(path string, b *Backends) (*Plan, error) {
	if b == nil {
		b = &Backends{}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("是个目录，本查看器只处理单个文件: " + abs)
	}

	p := &Plan{
		Path: abs,
		Name: filepath.Base(abs),
		Size: st.Size(),
		Ext:  strings.ToLower(filepath.Ext(abs)),
	}

	if st.Size() == 0 {
		p.Kind, p.Renderer, p.Degraded = KindText, RShell, DegEmptyFile
		return p, nil
	}

	switch {
	case extMarkdown[p.Ext]:
		p.Kind, p.Renderer = KindMarkdown, RShell
		p.loadText(abs)

	case extHTML[p.Ext]:
		// 直通：让 Chromium 用文件自己的 origin 渲染，最接近浏览器行为
		p.Kind, p.Renderer = KindHTML, RDirect

	case extPDF[p.Ext]:
		// PDF 走 WebView2 内置 viewer，但套在 iframe 里，这样菜单条和快捷键跟其他类型一致
		p.Kind, p.Renderer = KindPDF, RShell

	case extImageOK[p.Ext]:
		p.Kind, p.Renderer = KindImage, RShell

	case extImageBad[p.Ext]:
		p.Kind = KindImage
		p.degradeImage(b, "chromium 不认 "+p.Ext)

	case extVideoWeb[p.Ext]:
		p.Kind, p.Renderer = KindVideo, RMedia
		p.Container = strings.TrimPrefix(p.Ext, ".")
		p.Codec = sniffMP4Codec(abs, st.Size())
		if p.Codec == "" {
			p.Degraded = DegCodecUnknown
			p.Notes = append(p.Notes, "没在头部/尾部找到 moov 编码标识，按可播放处理，失败由前端报错兜底")
		} else if !chromiumCodec[p.Codec] {
			p.degradeVideo(b, DegCodec, "编码 "+codecLabel(p.Codec)+" Chromium 解不了")
		}

	case extVideoOther[p.Ext]:
		p.Kind = KindVideo
		p.Container = strings.TrimPrefix(p.Ext, ".")
		p.Codec = sniffMKVCodec(abs, st.Size())
		p.degradeVideo(b, DegContainer, "容器 ."+p.Container+" 不在 <video> 支持范围")

	case extAudioWeb[p.Ext]:
		p.Kind, p.Renderer = KindAudio, RMedia
		p.Container = strings.TrimPrefix(p.Ext, ".")

	case extAudioOther[p.Ext]:
		p.Kind = KindAudio
		p.Container = strings.TrimPrefix(p.Ext, ".")
		p.Degraded = DegContainer
		if pl := b.PickPlayer(false, true); pl != nil {
			p.Renderer, p.Backend = RExternal, pl.Path
		} else {
			p.Renderer, p.Backend, p.Degraded = RExternal, "", DegNoExternal
		}

	case extText[p.Ext] || p.Ext == "":
		p.Kind, p.Renderer = KindText, RShell
		p.loadText(abs)

	default:
		// 未知后缀：按内容嗅探一次，认得就按认得的路由，认不得落 hex
		head := readHead(abs, 512)
		switch {
		case len(head) == 0:
			p.Kind, p.Renderer, p.Degraded = KindBinary, RExternal, DegBinaryUnknown
		case looksText(head):
			p.Kind, p.Renderer = KindText, RShell
			p.loadText(abs)
		default:
			p.Kind, p.Renderer, p.Degraded = KindBinary, RShell, DegBinaryUnknown
			p.Text = hexDump(readHead(abs, 4096), 0)
		}
	}

	return p, nil
}

func (p *Plan) degradeImage(b *Backends, why string) {
	p.Degraded = DegImageFormat
	if pl := b.PickPlayer(true, false); pl != nil {
		p.Renderer, p.Backend = RExternal, pl.Path
		p.Notes = append(p.Notes, why+"，换解码器：交 "+pl.Name)
		return
	}
	p.Renderer, p.Backend, p.Degraded = RExternal, "", DegNoExternal
	p.Notes = append(p.Notes, why+"，且本机没有任何可用的外部播放器")
}

// degradeVideo 走"换播放引擎"这条路。reason 由调用方给定：
// 容器挡住的记 container，编码挡住的记 codec，先挡住的那个才是原因。
func (p *Plan) degradeVideo(b *Backends, reason, why string) {
	if pl := b.PickPlayer(true, false); pl != nil {
		p.Renderer, p.Backend, p.Degraded = RExternal, pl.Path, reason
		p.Notes = append(p.Notes, why+"，换播放引擎：交 "+pl.Name)
		return
	}
	p.Renderer, p.Backend, p.Degraded = RExternal, "", DegNoExternal
	p.Notes = append(p.Notes, why+"，且本机没有任何可用的外部播放器")
}

func (p *Plan) loadText(abs string) {
	b, truncated, err := readCapped(abs, textCap)
	if err != nil {
		p.Kind, p.Renderer, p.Error = KindBinary, RExternal, err.Error()
		p.Degraded = DegBinaryUnknown
		return
	}
	if !looksText(b) {
		// 后缀说是文本，内容其实是二进制 ⇒ 落 hex，不装能读
		p.Kind, p.Renderer, p.Degraded = KindBinary, RShell, DegBinaryUnknown
		p.Notes = append(p.Notes, "后缀是文本但内容含 NUL 字节，按二进制处理")
		p.Text = hexDump(head(b, 4096), 0)
		return
	}
	s := decodeBytes(b)
	if truncated {
		p.Truncated, p.Degraded = true, DegTextTruncated
	}
	p.Text = s
	p.Lines = strings.Count(s, "\n") + 1
}

func codecLabel(fourcc string) string {
	if n, ok := chromiumCodecName[fourcc]; ok {
		return n + "(" + fourcc + ")"
	}
	return fourcc
}

// DegradedLabel 给 UI 用的中文解释。
func DegradedLabel(p *Plan) string {
	switch p.Degraded {
	case DegNone:
		return ""
	case DegContainer:
		return "容器不支持：已换播放引擎"
	case DegCodec:
		return "编码不支持：已换播放引擎"
	case DegCodecUnknown:
		return "编码未识别：按可播放处理"
	case DegImageFormat:
		return "图片格式不支持：已换解码器"
	case DegTextTruncated:
		return fmt.Sprintf("文本超过 %d MiB，已截断", textCap>>20)
	case DegNoExternal:
		return "本机无可用外部播放器"
	case DegEmptyFile:
		return "空文件"
	case DegBinaryUnknown:
		return "二进制：仅十六进制预览"
	case DegDropNoPath:
		return "拖入：无路径，仅就地预览"
	}
	return p.Degraded
}
