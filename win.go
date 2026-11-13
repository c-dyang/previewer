package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procWTSGetActiveConsoleSessionId = modKernel32.NewProc("WTSGetActiveConsoleSessionId")
	procProcessIdToSessionId         = modKernel32.NewProc("ProcessIdToSessionId")
	procGetConsoleWindow             = modKernel32.NewProc("GetConsoleWindow")
	procGetConsoleProcessList        = modKernel32.NewProc("GetConsoleProcessList")

	procGetWindowLongPtrW   = modUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW   = modUser32.NewProc("SetWindowLongPtrW")
	procSetWindowPos        = modUser32.NewProc("SetWindowPos")
	procGetWindowRect       = modUser32.NewProc("GetWindowRect")
	procMonitorFromWindow   = modUser32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW     = modUser32.NewProc("GetMonitorInfoW")
	procIsZoomed            = modUser32.NewProc("IsZoomed")
	procShowWindow          = modUser32.NewProc("ShowWindow")
	procSetForegroundWindow = modUser32.NewProc("SetForegroundWindow")
)

// 为什么这里**不**做 SetProcessDpiAwarenessContext：
//
// 2026-11-13 实测（本机 3840x2160 @125%）：进程做成 PER_MONITOR_AWARE_V2 之后，
// WebView2 给出的视口是 innerWidth=1676 / dpr=1.25 —— 视口按物理像素给，却又按 1.25
// 栅格化，于是整页放大 25%、右边和下边被裁掉（状态栏文字直接看不见、居中文字偏右）。
// 根因：go-webview2 只把 GetClientRect 喂给 PutBounds，从不调 PutRasterizationScale，
// 布局用 1.0、合成用 1.25，两边对不上。
//
// 那个接口在 ICoreWebView2Controller2 上，本库没有暴露，controller 字段也不导出，
// 所以唯一稳的解法是让进程保持 DPI-unaware：坐标走系统虚拟化，视口和栅格化都按 1.0，
// 布局就是对的（代价是系统做 1.25 倍放大，字略软 —— 所有老的 Win32 程序都这样）。

// interactiveSession 判断本进程是否跑在"有桌面的交互会话"里。
//
// 背景（2026-11-13 在 PC 上实测）：经 SSH/服务进的是 Session 0，没有交互桌面。
// 这时 WebView2 的 CreateCoreWebView2Controller 会失败并回调 controller=nil，
// 而 go-webview2 只用 `int64(res) < 0` 判 HRESULT —— uintptr 装 0x8007xxxx 是正数，
// 于是判不出来，直接 controller.vtbl 空指针 panic。
// 所以进窗口前先自己判一次，给出可读原因而不是 Go 堆栈。
// 判据取「本进程是否在 Session 0」：Vista 起 Session 0 是服务会话，没有交互桌面。
// 不用「本会话 == WTSGetActiveConsoleSessionId」——RDP 会话 id 与控制台会话 id 不同，
// 那样会把正常的远程桌面误判成不能开窗。
func interactiveSession() (ok bool, consoleSID, mySID uint32) {
	active, _, _ := procWTSGetActiveConsoleSessionId.Call()
	consoleSID = uint32(active)
	var sid uint32
	procProcessIdToSessionId.Call(uintptr(os.Getpid()), uintptr(unsafe.Pointer(&sid)))
	mySID = sid
	return mySID != 0, consoleSID, mySID
}

// hideOwnConsole 把「自己独占的那个控制台窗口」藏起来。
//
// 为什么不直接用 `-ldflags "-H windowsgui"`：那会把进程变成 GUI 子系统，
// os.Stdout 直接失效，`--probe --json` 这条给 agent/脚本读的契约就废了。
// 所以保持 CUI 子系统（stdout 正常），只在"这个控制台是我自己新开的"时候隐藏它。
//
// 判据是 GetConsoleProcessList：控制台上只挂着本进程 ⇒ 是双击 / 快捷方式 /
// Start-Process 这类由系统新开的情况 ⇒ 藏；挂着调用方（从 cmd / pwsh 里跑）⇒ 返回 >1，
// 这时候绝不能藏 —— 否则会把用户自己的终端窗口一起藏掉，而且 --probe 的输出也没了。
func hideOwnConsole() {
	h, _, _ := procGetConsoleWindow.Call()
	if h == 0 {
		return // 压根没有控制台（被管道/服务拉起）
	}
	var pids [4]uint32
	n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if int32(n) <= 1 {
		procShowWindow.Call(h, swHide)
	}
}

// workArea 返回主显示器工作区（物理像素）。
func workArea() (x, y, w, h int) {
	mon, _, _ := procMonitorFromWindow.Call(0, monitorPrimary) // 无窗口 ⇒ 主显示器
	mi := winMonitorInfo{CbSize: uint32(unsafe.Sizeof(winMonitorInfo{}))}
	if r, _, _ := procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); r == 0 {
		return 0, 0, 1280, 720
	}
	return int(mi.RcWork.Left), int(mi.RcWork.Top),
		int(mi.RcWork.Right - mi.RcWork.Left), int(mi.RcWork.Bottom - mi.RcWork.Top)
}

// centerWindow 把窗口按工作区居中、并夹到工作区内。
// 自己算而不是用库里的 Center —— 库拿的是 SM_CXSCREEN，在高缩放比下不准。
func centerWindow(hwnd uintptr, w, h int) {
	if hwnd == 0 {
		return
	}
	ax, ay, aw, ah := workArea()
	if aw <= 0 || ah <= 0 {
		return
	}
	if w <= 0 || w > aw-40 {
		w = aw - 40
	}
	if h <= 0 || h > ah-40 {
		h = ah - 40
	}
	x := ax + (aw-w)/2
	y := ay + (ah-h)/2
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		swpNoZOrder|swpFrameChanged|swpShowWindow)
}

type winRect struct {
	Left, Top, Right, Bottom int32
}

// winMonitorInfo 对应 MONITORINFO，sizeof = 40。
type winMonitorInfo struct {
	CbSize    uint32
	RcMonitor winRect
	RcWork    winRect
	DwFlags   uint32
}

const (
	gwlpStyle       = ^uintptr(15) // GWL_STYLE = -16，64 位要符号扩展
	wsPopup         = 0x80000000
	wsVisible       = 0x10000000
	wsOverlapped    = 0x00CF0000 // WS_OVERLAPPEDWINDOW
	swpNoZOrder     = 0x0004
	swpFrameChanged = 0x0020
	swpShowWindow   = 0x0040
	swMaximize      = 3
	swHide          = 0
	monitorNearest  = 2 // MONITOR_DEFAULTTONEAREST
	monitorPrimary  = 1 // MONITOR_DEFAULTTOPRIMARY
)

type fsState struct {
	on     bool
	style  uintptr
	r      winRect
	zoomed bool
	live   bool // 有没有存过状态
}

var (
	fsMu sync.Mutex
	fs   fsState
)

// toggleFullscreen 做的是无边框全屏：清掉 WS_OVERLAPPEDWINDOW 换 WS_POPUP，
// 再把窗口摆到当前显示器整幅矩形上。退出时按保存的 style/rect 复原。
func toggleFullscreen(hwnd uintptr) (nowFull bool, ok bool) {
	if hwnd == 0 {
		return false, false
	}
	fsMu.Lock()
	defer fsMu.Unlock()

	if !fs.on {
		style, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlpStyle)
		procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&fs.r)))
		z, _, _ := procIsZoomed.Call(hwnd)
		fs.style, fs.zoomed, fs.live = style, z != 0, true

		newStyle := (style &^ uintptr(wsOverlapped)) | wsPopup | wsVisible
		procSetWindowLongPtrW.Call(hwnd, gwlpStyle, newStyle)

		mon, _, _ := procMonitorFromWindow.Call(hwnd, monitorNearest)
		mi := winMonitorInfo{CbSize: uint32(unsafe.Sizeof(winMonitorInfo{}))}
		procGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi)))
		r := mi.RcMonitor
		procSetWindowPos.Call(hwnd, 0,
			uintptr(int(r.Left)), uintptr(int(r.Top)),
			uintptr(int(r.Right-r.Left)), uintptr(int(r.Bottom-r.Top)),
			swpNoZOrder|swpFrameChanged|swpShowWindow)
		fs.on = true
		return true, true
	}

	if fs.live {
		procSetWindowLongPtrW.Call(hwnd, gwlpStyle, fs.style)
		if fs.zoomed {
			procShowWindow.Call(hwnd, swMaximize)
		} else {
			r := fs.r
			procSetWindowPos.Call(hwnd, 0,
				uintptr(int(r.Left)), uintptr(int(r.Top)),
				uintptr(int(r.Right-r.Left)), uintptr(int(r.Bottom-r.Top)),
				swpNoZOrder|swpFrameChanged|swpShowWindow)
		}
	}
	fs.on = false
	return false, true
}

// revealInExplorer 打开资源管理器并选中该文件。
func revealInExplorer(path string) error {
	return exec.Command("explorer.exe", "/select,"+path).Start()
}

// shellOpen 用系统关联打开。没有关联时退到"打开方式"对话框。
func shellOpen(hwnd uintptr, path string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.ShellExecute(windows.Handle(hwnd), verb, file, nil, nil, 1); err != nil {
		return exec.Command("rundll32.exe", "shell32.dll,OpenAs_RunDLL", path).Start()
	}
	return nil
}

// spawnPlayer 把文件交给外部播放器（降级链的终点）。
func spawnPlayer(player, path string) error {
	cmd := exec.Command(player, path)
	return cmd.Start()
}

// copyToClipboard 走 PowerShell：路径含中文/空格时用单引号字面量，避免解析歧义。
func copyToClipboard(path string) error {
	lit := "'" + strings.ReplaceAll(path, "'", "''") + "'"
	return exec.Command("powershell.exe", "-NoProfile", "-STA", "-WindowStyle", "Hidden",
		"-Command", "Set-Clipboard -LiteralPath "+lit).Start()
}

// pickFile 用 Windows 标准打开对话框选文件。
// v1 故意不手写 OPENFILENAMEW（结构体布局一错就是永久静默失败，
// 而这台机器上没法自动点对话框验证），改用 PowerShell 里的 WinForms 对话框。
func pickFile() (string, error) {
	const ps = `Add-Type -AssemblyName System.Windows.Forms;` +
		`$d=New-Object System.Windows.Forms.OpenFileDialog;` +
		`$d.Filter='所有文件|*.*';` +
		`$d.CheckFileExists=$true;` +
		`if($d.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK){[Console]::Out.Write($d.FileName)}`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-WindowStyle", "Hidden", "-Command", ps)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// ---------- 渲染页临时文件 ----------

func viewDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "previewer", "view")
}

func clearViewDir() {
	dir := viewDir()
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// writeViewHTML 把外壳页落盘再 Navigate。
// 关键点：必须以 file:// 页面的身份加载，否则 <img>/<video> 引用本地文件
// 会被 Chromium 的同源策略挡掉（about:blank 源读不到 file://）。
func writeViewHTML(h string) (string, error) {
	dir := viewDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "view-*.html")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(h); err != nil {
		return "", err
	}
	return fileURL(f.Name()), nil
}
