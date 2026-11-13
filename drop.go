package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 文件拖进来的两条通路，谁先到就用谁：
//
//	① JS 侧 drop 事件：从 dataTransfer 里抠路径。Chromium 不给 File.path
//	   （那是 Electron 私有），唯一可能带全路径的是 text/uri-list。
//	② Win32 侧 WM_DROPFILES：拖到标题栏 / 非客户区，或者 Chromium 没吃掉的
//	   情况会落到顶层窗口。要 DragAcceptFiles + 子类化 WndProc 才能收到。
//
// 两条都实现是因为哪条真的会响，只能在真机上拖一次才知道 —— 与其赌，不如都挂上。

var (
	modShell32 = windows.NewLazySystemDLL("shell32.dll")

	procDragAcceptFiles = modShell32.NewProc("DragAcceptFiles")
	procDragQueryFileW  = modShell32.NewProc("DragQueryFileW")
	procDragFinish      = modShell32.NewProc("DragFinish")
	procCallWindowProcW = modUser32.NewProc("CallWindowProcW")
)

const (
	gwlpWndProc = ^uintptr(3) // GWLP_WNDPROC = -4
	wmDropFiles = 0x0233
)

var (
	dropOldProc  uintptr
	dropCallback uintptr
	dropHandler  func([]string)
)

// installDrop 在顶层窗口上装 WM_DROPFILES。子类化必须把不属于自己的消息
// 原样转给原窗口过程，否则整个窗口（含 WebView2 的尺寸/焦点逻辑）会失灵。
func installDrop(hwnd uintptr, onDrop func([]string)) {
	if hwnd == 0 || onDrop == nil {
		return
	}
	dropHandler = onDrop
	dropCallback = windows.NewCallback(func(h, msg, wp, lp uintptr) uintptr {
		if msg == wmDropFiles {
			paths := hdropPaths(wp)
			procDragFinish.Call(wp)
			if len(paths) > 0 && dropHandler != nil {
				dropHandler(paths)
			}
			return 0
		}
		r, _, _ := procCallWindowProcW.Call(dropOldProc, h, msg, wp, lp)
		return r
	})
	procDragAcceptFiles.Call(hwnd, 1)
	old, _, _ := procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, dropCallback)
	if old != 0 {
		dropOldProc = old
	}
}

// hdropPaths 把 HDROP 里的文件列表掏出来（多选拖拽会有多条）。
func hdropPaths(hdrop uintptr) []string {
	n, _, _ := procDragQueryFileW.Call(hdrop, ^uintptr(0), 0, 0) // 0xFFFFFFFF = 取数量
	count := int(int32(n))
	if count <= 0 {
		return nil
	}
	buf := make([]uint16, 32768) // MAX_PATH 太小，长路径会截断
	out := make([]string, 0, count)
	for i := 0; i < count; i++ {
		r, _, _ := procDragQueryFileW.Call(hdrop, uintptr(i),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if int32(r) <= 0 {
			continue
		}
		out = append(out, windows.UTF16ToString(buf[:int(int32(r))]))
	}
	return out
}

// ---------- JS 侧 drop 载荷 ----------

type dropFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	Path string `json:"path"` // Chromium 里通常是空串，留着是为了万一
}

type dropPayload struct {
	Types []string          `json:"types"`
	Data  map[string]string `json:"data"`
	Files []dropFile        `json:"files"`
}

func parseDropPayload(s string) (*dropPayload, error) {
	var p dropPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// pickDropPath 按可信度挑选路径：File.path（若有）> text/uri-list > text/plain。
func pickDropPath(p *dropPayload) (string, bool) {
	if p == nil {
		return "", false
	}
	for _, f := range p.Files {
		if isAbsoluteLocal(f.Path) {
			return f.Path, true
		}
	}
	for _, line := range strings.Split(p.Data["text/uri-list"], "\n") {
		if pp, ok := urlToPath(line); ok {
			return pp, true
		}
	}
	if t := strings.TrimSpace(p.Data["text/plain"]); t != "" && isAbsoluteLocal(t) {
		if st, err := os.Stat(t); err == nil && !st.IsDir() {
			return t, true
		}
	}
	return "", false
}

func isAbsoluteLocal(s string) bool {
	if s == "" || strings.Contains(s, "\x00") {
		return false
	}
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return strings.HasPrefix(s, `\\`)
}

// urlToPath 把 file:// 转回本地路径。非 file: 一律不接（http/data 拖进来没意义）。
func urlToPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", false
	}
	if !strings.HasPrefix(strings.ToLower(raw), "file:") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	p := u.Path
	if p == "" {
		p = strings.TrimPrefix(raw, "file:")
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		// file://server/share/x → \\server\share\x
		return `\\` + u.Host + filepath.FromSlash(p), true
	}
	p = strings.TrimPrefix(p, "/") // /C:/x → C:/x
	if !isAbsoluteLocal(filepath.FromSlash(p)) {
		return "", false
	}
	return filepath.FromSlash(p), true
}
