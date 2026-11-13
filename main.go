package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

const appVersion = "0.2.0"

// 退出码：0 正常 / 2 用法 / 3 缺 WebView2 / 4 目标打不开 / 6 起窗口失败 / 7 无外部播放器 / 8 无交互桌面
const (
	exitOK        = 0
	exitUsage     = 2
	exitNoWebView = 3
	exitBadTarget = 4
	exitWindow    = 6
	exitNoBackend = 7
	exitNoDesktop = 8
)

// globalJS 注入到每一次页面创建，包括直通渲染的 file:// 页面。
// 直通路由（html/pdf）没有我们的外壳，靠它兜住 Esc / F11。
const globalJS = `(function(){
  if (window.__pvGlobal) return; window.__pvGlobal = true;
  window.addEventListener('keydown', function(e){
    if (window.__pvShell) return;
    if (e.key === 'Escape') { e.preventDefault(); try{ pvAct('quit'); }catch(_){} }
    else if (e.key === 'F11') { e.preventDefault(); try{ pvAct('fullscreen'); }catch(_){} }
  }, true);
})();`

type App struct {
	w    webview2.WebView
	hwnd uintptr
	b    *Backends

	mu   sync.Mutex
	plan *Plan
}

func main() {
	runtime.LockOSThread() // 窗口、消息循环、WebView2 必须同一 OS 线程

	var (
		probe     = flag.Bool("probe", false, "只探测能力与路由，不开窗口")
		asJSON    = flag.Bool("json", false, "以 JSON 输出（配合 --probe）")
		debug     = flag.Bool("debug", false, "开 WebView2 调试（DevTools / 右键菜单）")
		width     = flag.Int("width", 0, "窗口宽（默认 1100）")
		height    = flag.Int("height", 0, "窗口高（默认 760）")
		startFS   = flag.Bool("fullscreen", false, "全屏启动")
		forceExt  = flag.Bool("external", false, "强制交给外部程序")
		autoClose = flag.Int("auto-close", 0, "N 秒后自动关窗（脚本化冒烟用，0=不自动关）")
		dropSim   = flag.String("drop-sim", "", "冒烟钩子：把该文件当作「拖进画面」处理，走就地渲染链路")
		ver       = flag.Bool("version", false, "输出版本")
	)
	flag.Usage = usage
	flag.Parse()

	// 双击裸开时别甩一个黑框 cmd 窗口出来；--probe 是给人/agent 读 stdout 的，绝不藏
	if !*probe && !*ver {
		hideOwnConsole()
	}

	if *ver {
		fmt.Println("previewer", appVersion)
		return
	}

	if *probe {
		os.Exit(runProbe(flag.Args(), *asJSON))
	}

	args := flag.Args()

	b := Probe()

	// 裸开（不带文件）：给一个空壳窗口，等文件拖进来 / Ctrl+O。
	// 双击 exe 和从右键菜单带参数进来走的是同一条路径，只是目标不同。
	var p *Plan
	if len(args) == 0 {
		p = EmptyPlan()
	} else {
		var err error
		p, err = Resolve(args[0], b)
		if err != nil {
			fmt.Fprintln(os.Stderr, "previewer: "+err.Error())
			os.Exit(exitBadTarget)
		}
	}

	if *forceExt {
		p.Renderer = RExternal
		p.Backend = ""
		if pl := b.PickPlayer(true, true); pl != nil {
			p.Backend = pl.Path
		}
	}

	if p.Renderer == RExternal {
		if p.Backend == "" {
			fmt.Fprintf(os.Stderr,
				"previewer: %s 需要外部播放器，但本机没找到（候选：mpv / VLC / PotPlayer / MPC-BE / MPC-HC / ffplay）\n",
				filepath.Base(p.Path))
			os.Exit(exitNoBackend)
		}
		if err := spawnPlayer(p.Backend, p.Path); err != nil {
			fmt.Fprintln(os.Stderr, "previewer: 启动外部播放器失败: "+err.Error())
			os.Exit(exitNoBackend)
		}
		fmt.Printf("%s\t-> %s\n", p.Path, p.Backend)
		return
	}

	if !b.WebView2.Present {
		fmt.Fprintln(os.Stderr, "previewer: 没有可用的 WebView2 Runtime，无法开窗口。用 --probe --json 看细节。")
		os.Exit(exitNoWebView)
	}

	// WebView2 必须有交互桌面。经 SSH / 服务进的是 Session 0，建不出 controller，
	// 而 go-webview2 在那种情况下会空指针 panic —— 提前拦住，给一句人话。
	if ok, consoleSID, mySID := interactiveSession(); !ok {
		fmt.Fprintf(os.Stderr,
			"previewer: 当前进程在会话 %d（Session 0 = 无交互桌面的服务会话；控制台会话 %d）。\n"+
				"           WebView2 需要交互式桌面，请在目标机的桌面上双击运行；只查路由可用 --probe --json。\n",
			mySID, consoleSID)
		os.Exit(exitNoDesktop)
	}

	a := &App{b: b}
	if err := a.open(p, *width, *height, *debug, *startFS); err != nil {
		fmt.Fprintln(os.Stderr, "previewer: "+err.Error())
		os.Exit(exitWindow)
	}
	if *autoClose > 0 {
		// 给远程/脚本化冒烟用：窗口自己走完就走，不留残窗
		d := time.Duration(*autoClose) * time.Second
		time.AfterFunc(d, func() {
			a.w.Dispatch(func() { a.w.Destroy() })
		})
	}
	if *dropSim != "" {
		// 等壳页 Navigate 完再注入，否则 Eval 打在 about:blank 上白打
		time.AfterFunc(1500*time.Millisecond, func() { a.simulateDrop(*dropSim) })
	}
	a.w.Run()
}

// simulateDrop 是 --drop-sim 的实现：把本地文件伪装成"拖进画面"的载荷。
// 唯一区别是没有真的 blob: URL（改用 file://），其余字段与浏览器给的一致，
// 所以它能覆盖 ResolveName + contentFor + pvReplace 整条就地渲染链。
func (a *App) simulateDrop(path string) {
	st, err := os.Stat(path)
	if err != nil {
		a.logDrop("drop-sim 文件不存在: " + path)
		return
	}
	d := droppedBlob{Name: st.Name(), Size: st.Size()}
	if b, truncated, err := readCapped(path, textCap); err == nil && looksText(b) {
		// 刻意走 b64 而不是 text：浏览器那边传的就是原始字节，模拟也要走同一条解码路
		d.B64 = base64.StdEncoding.EncodeToString(b)
		d.Truncated = truncated
	} else {
		d.URL = fileURL(path)
	}
	raw, _ := json.Marshal(d)
	a.handleDroppedBlob(string(raw))
}

func usage() {
	fmt.Fprintf(os.Stderr, `preview.exe %s —— 单窗口万能预览器（借鉴 Adobe Flash Player 独立播放器：标题栏 + 细菜单条 + 画布）

用法（选项必须写在文件路径之前，Go flag 遇到第一个非选项参数就停止解析）:
  preview.exe <文件>
  preview.exe --probe --json [文件]        # 只看探测结果，不开窗口
  preview.exe --external <文件>            # 强制外部程序
  preview.exe --auto-close 5 <文件>        # 5 秒后自动关窗（脚本化冒烟）
选项:
  --debug         开 DevTools 与右键菜单
  --width N       窗口宽（默认取工作区 55%%）
  --height N      窗口高（默认取工作区 75%%）
  --fullscreen    全屏启动
  --auto-close N  N 秒后自动关窗，0=不自动关
  --version       输出版本
退出码:
  0 正常 · 2 用法错 · 3 缺 WebView2 · 4 目标打不开 · 6 起窗口失败 · 7 无外部播放器 · 8 无交互桌面
`, appVersion)
}

// ---------- probe ----------

func runProbe(args []string, asJSON bool) int {
	b := Probe()
	rep := &ProbeReport{OK: b.WebView2.Present, SchemaVersion: 1, Backends: b}

	if len(args) > 0 {
		p, err := Resolve(args[0], b)
		if err != nil {
			rep.OK = false
			rep.Error = err.Error()
			if asJSON {
				fmt.Println(rep.JSON())
			} else {
				fmt.Fprintln(os.Stderr, "previewer: "+err.Error())
			}
			return exitBadTarget
		}
		rep.Target = p
		rep.Degraded = p.Degraded
	}

	if asJSON {
		fmt.Println(rep.JSON())
	} else {
		printProbeHuman(rep)
	}
	if !b.WebView2.Present {
		return exitNoWebView
	}
	return exitOK
}

func printProbeHuman(rep *ProbeReport) {
	b := rep.Backends
	fmt.Printf("WebView2      : present=%v version=%s\n", b.WebView2.Present, b.WebView2.Version)
	if b.WebView2.RuntimeDir != "" {
		fmt.Printf("  runtime dir : %s\n", b.WebView2.RuntimeDir)
	}
	fmt.Printf("  来源        : %s\n", b.WebView2.Source)
	for _, n := range b.WebView2.Notes {
		fmt.Printf("  备注        : %s\n", n)
	}
	fmt.Printf("外部播放器    : %d 个\n", len(b.Players))
	for _, p := range b.Players {
		fmt.Printf("  - %-12s %s\n", p.Name, p.Path)
	}
	if rep.Target != nil {
		t := rep.Target
		fmt.Printf("目标          : %s\n", t.Path)
		fmt.Printf("  大小/类型   : %d B / %s / .%s\n", t.Size, t.Kind, strings.TrimPrefix(t.Ext, "."))
		fmt.Printf("  渲染器      : %s\n", t.Renderer)
		if t.Container != "" || t.Codec != "" {
			fmt.Printf("  容器/编码   : %s / %s\n", t.Container, t.Codec)
		}
		if t.Degraded != DegNone {
			fmt.Printf("  降级        : %s（%s）\n", t.Degraded, DegradedLabel(t))
		}
		for _, n := range t.Notes {
			fmt.Printf("  说明        : %s\n", n)
		}
	}
}

// ---------- 窗口 ----------

func (a *App) open(p *Plan, w, h int, debug, fullscreen bool) (err error) {
	// go-webview2 只看 `int64(res) < 0` 判 HRESULT，而 uintptr 装 0x8007xxxx 是正数，
	// 判不出来就直奔 controller.vtbl → 空指针 panic。这里兜一层：
	// 任何 WebView2 初始化异常都以「一句可读原因 + 退出码 6」收场，绝不吐 Go 堆栈给调用方。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("WebView2 初始化崩溃（controller 未创建，常见于非交互会话或运行时异常）: %v", r)
		}
	}()
	_, _, aw, ah := workArea()
	// 进程已是 per-monitor-v2 DPI 感知，这里的单位就是物理像素。
	// 按工作区的比例给默认尺寸：4K 上 1180 会小得像邮票，1080p 上又不能爆屏。
	if w <= 0 {
		w = aw * 55 / 100
		if w < 1000 {
			w = 1000
		}
		if aw > 0 && w > aw-80 {
			w = aw - 80
		}
	}
	if h <= 0 {
		h = ah * 75 / 100
		if h < 700 {
			h = 700
		}
		if ah > 0 && h > ah-80 {
			h = ah - 80
		}
	}
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	dataPath := filepath.Join(base, "previewer", "WebView2")
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return errors.New("WebView2 数据目录不可写: " + dataPath)
	}

	vw := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     debug,
		DataPath:  dataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  p.Name,
			Width:  uint(w),
			Height: uint(h),
			Center: false, // 自己居中（见 centerWindow）
		},
	})
	if vw == nil {
		return errors.New("WebView2 初始化失败（运行时不可用或数据目录被占用）")
	}
	a.w = vw
	a.hwnd = uintptr(vw.Window())
	centerWindow(a.hwnd, w, h)

	if err := vw.Bind("pvAct", a.act); err != nil {
		return errors.New("绑定 JS→Go 通道失败: " + err.Error())
	}
	vw.Init(globalJS)

	// 拖到标题栏 / 非客户区的文件走 WM_DROPFILES；拖到页面里走 JS drop。
	installDrop(a.hwnd, func(paths []string) { a.openPaths(paths) })

	if fullscreen {
		toggleFullscreen(a.hwnd)
	}
	return a.load(p)
}

func (a *App) cur() *Plan {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.plan
}

func (a *App) load(p *Plan) error {
	a.mu.Lock()
	a.plan = p
	a.mu.Unlock()

	a.w.SetTitle(p.Name)

	if p.Renderer == RDirect {
		// 直通：html/pdf 让 Chromium 用自己的 viewer 渲染，不套外壳
		a.w.Navigate(fileURL(p.Path))
		return nil
	}
	u, err := writeViewHTML(buildShell(p))
	if err != nil {
		return errors.New("写渲染页失败: " + err.Error())
	}
	a.w.Navigate(u)
	return nil
}

// act 是 JS 侧 pvAct(id) 的唯一入口。
func (a *App) act(id string) string {
	p := a.cur()
	hasTarget := p != nil && p.Path != ""

	switch {
	case id == "quit":
		a.w.Destroy()
	case id == "fullscreen":
		toggleFullscreen(a.hwnd)
	case strings.HasPrefix(id, "drop:"):
		a.handleDropPayload(id[len("drop:"):])
	case strings.HasPrefix(id, "dropblob:"):
		a.handleDroppedBlob(id[len("dropblob:"):])
	case strings.HasPrefix(id, "replace-done:"):
		a.logDrop("pvReplace 落地 " + id[len("replace-done:"):])
	case strings.HasPrefix(id, "img-probe:"):
		a.logDrop("图片加载 " + id[len("img-probe:"):])
	case id == "reveal":
		if hasTarget {
			_ = revealInExplorer(p.Path)
		}
	case id == "copy":
		if hasTarget {
			_ = copyToClipboard(p.Path)
		}
	case id == "external":
		if hasTarget {
			hwnd := a.hwnd
			path := p.Path
			go func() { _ = shellOpen(hwnd, path) }()
		}
	case id == "open":
		go a.pickAndLoad()
	case id == "info":
		if hasTarget {
			a.showPage("文件信息："+p.Name, infoBody(p, a.b))
		}
	case id == "about":
		a.showPage("关于 previewer", aboutBody(a.b))
	case id == "back":
		if p != nil {
			_ = a.load(p)
		}
	case strings.HasPrefix(id, "zoom-changed:"):
		// 前端自己管缩放，这里只是留痕入口
	default:
		// 媒体控制、缩放这类纯前端动作回灌给页面
		a.w.Eval("pvDo(" + strconv.Quote(id) + ")")
	}
	return "ok"
}

// handleDropPayload 处理 JS 侧 drop 事件带上来的载荷。
// 取不到路径时把载荷原样落盘，方便下次照着实测结果改提取策略。
func (a *App) handleDropPayload(raw string) {
	dp, err := parseDropPayload(raw)
	if err != nil {
		a.logDrop("解析失败: " + err.Error() + "\n" + raw)
		return
	}
	if path, ok := pickDropPath(dp); ok {
		a.logDrop("命中路径: " + path)
		a.openPaths([]string{path})
		return
	}
	a.logDrop("拿不到路径，原始载荷:\n" + raw)
}

// droppedBlob 是"拖到画面里"带上来的东西：只有名字和内容/blob URL，没有路径。
type droppedBlob struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Type      string `json:"type"`
	Text      string `json:"text"`
	B64       string `json:"b64"` // 文本文件传原始字节，解码在 Go 这边做
	URL       string `json:"url"`
	Truncated bool   `json:"truncated"`
}

// handleDroppedBlob 走"就地渲染"：Go 判类型、造内容，JS 把 #cv 换掉。
// 不重载页面 —— blob: URL 只活在当前文档里，一 Navigate 就失效。
func (a *App) handleDroppedBlob(raw string) {
	var d droppedBlob
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		a.logDrop("dropblob 解析失败: " + err.Error() + "\n" + raw)
		return
	}
	if d.Name == "" {
		a.logDrop("dropblob 缺文件名")
		return
	}

	p := ResolveName(d.Name, d.Size, a.b)
	p.BlobURL = d.URL

	text := d.Text
	if text == "" && d.B64 != "" {
		if raw, err := base64.StdEncoding.DecodeString(d.B64); err == nil {
			text = decodeBytes(raw) // BOM → UTF-8 → GBK，和读盘那条路同一套
		} else {
			a.logDrop("base64 解不开: " + err.Error())
		}
	}
	if text != "" {
		p.Text = text
		p.Lines = strings.Count(text, "\n") + 1
	}
	if d.Truncated {
		p.Truncated = true
		p.Degraded = DegTextTruncated
		p.Notes = append(p.Notes, fmt.Sprintf("超过 %d MiB，只读了前 %d MiB", textCap>>20, textCap>>20))
	}
	a.logDrop(fmt.Sprintf("就地渲染 %s kind=%s blob=%v text=%dB", d.Name, p.Kind, d.URL != "", len(d.Text)))
	a.replaceContent(p)
}

// replaceContent 把已经建好页面的窗口就地换内容（拖拽专用）。
func (a *App) replaceContent(p *Plan) {
	a.mu.Lock()
	a.plan = p
	a.mu.Unlock()
	a.w.SetTitle(p.Name)

	meta, _ := json.Marshal(map[string]any{
		"name":     p.Name,
		"size":     p.Size,
		"degraded": p.Degraded,
		"blob":     p.BlobURL != "",
	})
	hasMedia := p.Kind == KindVideo || p.Kind == KindAudio
	script := "window.pvReplace && pvReplace(" +
		strconv.Quote(contentFor(p)) + "," +
		strconv.Quote(string(p.Kind)) + "," +
		strconv.Quote(statusHTML(p)) + "," +
		strconv.FormatBool(p.Path == "") + "," +
		strconv.FormatBool(hasMedia) + "," +
		string(meta) + ")"
	a.logDrop(fmt.Sprintf("Eval %d 字节, 前缀: %.80s", len(script), script))
	// WebView2 的 CoreWebView2 是单元线程对象：ExecuteScript 只能在创建它的 UI 线程上调，
	// 从别的 goroutine 直接调会静默失败（返回 RPC_E_WRONG_THREAD 而库里不看返回值）。
	a.w.Dispatch(func() { a.w.Eval(script) })
}

// openPaths 统一入口：拖拽（JS / WM_DROPFILES）和 Ctrl+O 都走这里。
// 多选只开第一个，其余在状态里点出来 —— 一次一个窗口是这个工具的边界。
func (a *App) openPaths(paths []string) {
	if len(paths) == 0 {
		return
	}
	p, err := Resolve(paths[0], a.b)
	if err != nil {
		a.logDrop("Resolve 失败 " + paths[0] + ": " + err.Error())
		return
	}
	if p.Renderer == RExternal {
		if p.Backend != "" {
			_ = spawnPlayer(p.Backend, p.Path)
		}
		return
	}
	if len(paths) > 1 {
		p.Notes = append(p.Notes, itoa(len(paths)-1)+" 个文件被忽略（一次只看一个）")
	}
	_ = a.load(p)
}

func (a *App) logDrop(msg string) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "previewer")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "drop.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

func (a *App) pickAndLoad() {
	fp, err := pickFile()
	if err != nil || fp == "" {
		return
	}
	// 选择器在自己起的 goroutine 里，回到 UI 线程再换页
	a.w.Dispatch(func() { a.openPaths([]string{fp}) })
}

func (a *App) showPage(title, body string) {
	u, err := writeViewHTML(docHTML(title, body))
	if err != nil {
		return
	}
	a.w.Navigate(u)
}

// ---------- 静态页 ----------

func docHTML(title, body string) string {
	return `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>` + escape(title) +
		`</title><style>
body{margin:0;background:#fff;color:#222;font:13px/1.7 "Segoe UI","Microsoft YaHei",sans-serif}
#bar{position:fixed;top:0;left:0;right:0;height:22px;background:#f0f0f0;border-bottom:1px solid #a0a0a0;
  display:flex;align-items:center;padding:0 9px;gap:12px;user-select:none}
#bar a{color:#0b62c4;text-decoration:none;cursor:pointer;font-size:12px}
#bar a:hover{text-decoration:underline}
#bar .sp{flex:1}
#bar .st{color:#777}
main{max-width:880px;margin:0 auto;padding:38px 30px 60px}
h1{font-size:17px;margin:0 0 18px;font-weight:600}
table{border-collapse:collapse;width:100%;font-size:12.5px}
td{padding:5px 10px;border-bottom:1px solid #eee;vertical-align:top;word-break:break-all}
td:first-child{width:150px;color:#666;white-space:nowrap}
b.warn{color:#b06a00}
code{background:#f2f2f2;padding:1px 5px;border-radius:3px;font-family:Consolas,monospace}
.mut{color:#888}
</style></head><body>
<div id="bar"><a onclick="try{pvAct('back')}catch(e){}">← 返回</a><span class="st">` + escape(title) +
		`</span><span class="sp"></span><a onclick="try{pvAct('quit')}catch(e){}">关闭</a></div>
<main>` + body + `</main></body></html>`
}

func infoBody(p *Plan, b *Backends) string {
	if p == nil {
		return `<p class="mut">没有目标。</p>`
	}
	row := func(k, v string) string {
		return "<tr><td>" + escape(k) + "</td><td>" + v + "</td></tr>"
	}
	var sb strings.Builder
	sb.WriteString(`<h1>` + escape(p.Name) + `</h1><table>`)
	sb.WriteString(row("完整路径", `<code>`+escape(p.Path)+`</code>`))
	sb.WriteString(row("大小", humanSize(p.Size)+"（"+strconv.FormatInt(p.Size, 10)+" 字节）"))
	sb.WriteString(row("类型", string(p.Kind)+" / ."+strings.TrimPrefix(p.Ext, ".")))
	sb.WriteString(row("渲染器", string(p.Renderer)))
	if p.Container != "" {
		sb.WriteString(row("容器", p.Container))
	}
	if p.Codec != "" {
		sb.WriteString(row("编码", codecLabel(p.Codec)+"（Chromium 原生支持: "+boolText(chromiumCodec[p.Codec])+"）"))
	}
	if p.Degraded != DegNone {
		sb.WriteString(row("降级", `<b class="warn">`+escape(p.Degraded)+`</b><br>`+escape(DegradedLabel(p))))
	}
	if p.Backend != "" {
		sb.WriteString(row("后端", `<code>`+escape(p.Backend)+`</code>`))
	}
	if p.Kind == KindText || p.Kind == KindMarkdown {
		sb.WriteString(row("行数", strconv.Itoa(p.Lines)+boolText0(p.Truncated, "（已截断）")))
	}
	if len(p.Notes) > 0 {
		var ns []string
		for _, n := range p.Notes {
			ns = append(ns, escape(n))
		}
		sb.WriteString(row("说明", strings.Join(ns, "<br>")))
	}
	sb.WriteString(`</table>`)
	_ = b
	return sb.String()
}

func aboutBody(b *Backends) string {
	row := func(k, v string) string {
		return "<tr><td>" + escape(k) + "</td><td>" + v + "</td></tr>"
	}
	var sb strings.Builder
	sb.WriteString(`<h1>previewer ` + appVersion + `</h1><table>`)
	sb.WriteString(row("定位", "单窗口万能预览器：一次调用一个窗口，看完即关，不常驻、不占后台"))
	sb.WriteString(row("外壳", "22px 菜单条 + 铺满画布，无工具栏、无侧栏（借鉴 Adobe Flash Player 独立播放器）"))
	sb.WriteString(row("快捷键", "<code>Esc</code> 关闭 · <code>F11</code> 全屏 · <code>Space</code> 播放/暂停 · <code>←/→</code> 前后 5 秒 · <code>↑/↓</code> 音量 · <code>Ctrl+O</code> 打开 · <code>Ctrl+E</code> 定位文件"))
	sb.WriteString(row("WebView2", escape(b.WebView2.Version)+"（"+escape(b.WebView2.Source)+"）"))
	sb.WriteString(row("外部后端", playerList(b)))
	sb.WriteString(row("降级原则", "每条降级都必须带可枚举的 degraded_reason；--probe --json 可直接读出"))
	sb.WriteString(`</table>`)
	return sb.String()
}

func playerList(b *Backends) string {
	if len(b.Players) == 0 {
		return `<span class="mut">未找到</span>`
	}
	var out []string
	for _, p := range b.Players {
		out = append(out, escape(p.Name)+` <code>`+escape(p.Path)+`</code>`)
	}
	return strings.Join(out, "<br>")
}

func boolText(v bool) string {
	if v {
		return "是"
	}
	return "否"
}

func boolText0(v bool, s string) string {
	if v {
		return s
	}
	return ""
}
