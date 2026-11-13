package main

// hostdrop.go —— 画面拖拽拿绝对路径。
//
// Chromium 的 HTML5 drop 只给 {name,type,size}，没有 File.path（那是 Electron 的私有扩展）。
// WebView2 在宿主侧留了一个官方出口（WebView2Feedback#501 → WebMessageObjects）：
//
//	页面： chrome.webview.postMessageWithAdditionalObjects(msg, [file])
//	宿主： ICoreWebView2WebMessageReceivedEventArgs2.GetAdditionalObjects
//	       -> ICoreWebView2ObjectCollectionView.GetValueAtIndex
//	       -> ICoreWebView2File.GetPath()   ← 绝对路径
//
// 版本门槛：args2 需要 WebView2 1.0.1774.30+。
//
// 为什么要自己写这几十行 COM：本项目用的 jchv/go-webview2 门面只暴露 12 个字符串级方法，
// 这三个接口一个都没声明；但它的 pkg/edge 是公开包，且 (*edge.Chromium).GetICoreWebView2_3()
// 已导出 —— 于是不需要 fork 库，reflect 拿到 Chromium 之后直接按 COM ABI 调槽位即可。
//
// 槽位是怎么定的（0 基，IUnknown 占 0/1/2）：
//   ICoreWebView2.AddWebMessageReceived        = 34（IDL 第 31 项 + 3）
//   ICoreWebView2WebMessageReceivedEventArgs2.GetAdditionalObjects = 6（基接口 3 个方法 + 3）
//   ICoreWebView2ObjectCollectionView.GetCount / GetValueAtIndex   = 3 / 4
//   ICoreWebView2File.GetPath                                      = 3
// 六号槽位是**实测**过的：Wails 生成式绑定里写的是 3，那是基接口 GetSource 的位置，不对。

import (
	"errors"
	"reflect"
	"syscall"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

const (
	slotAddWebMessageReceived = 34
	slotGetAdditionalObjects  = 6
	slotGetCount              = 3
	slotGetValueAtIndex       = 4
	slotGetPath               = 3
	slotRelease               = 2

	hrNoInterface = 0x80004002
)

// 这条消息的内容会被门面自己的 postMessage 处理器当成 RPC 调用解析一次。
// 写成合法 JSON 且方法名不存在 → callbinding 返回 (nil,nil)，不会往 stderr 打
// "invalid RPC message"（纯文本消息会打，实测过）。真正的载荷走 additional objects。
const hostDropMessage = `{"id":0,"method":"pvDropPath","params":null}`

var iidWebMessageReceivedEventArgs2 = edge.NewGUID("{06fc7ab7-c90c-4297-9389-33ca01cf6d5e}")

// comCall 按 0 基槽位调用 vtable 上的方法（this 自动放在第 0 个参数）。
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	if obj == nil {
		return hrNoInterface
	}
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func hrOK(hr uintptr) bool { return int32(hr) >= 0 }

// uintptrToPtr 把回调里拿到的 uintptr 还原成指针。
// 不能直接写 unsafe.Pointer(u)：go vet 的 unsafeptr 会判「possible misuse」。
// 走 &u 再读回是合法转换（指针→Pointer），值本身由 COM 或全局 keepalive 保证存活。
func uintptrToPtr(u uintptr) unsafe.Pointer {
	return *(*unsafe.Pointer)(unsafe.Pointer(&u))
}

type hostDropVtbl struct {
	queryInterface edge.ComProc
	addRef         edge.ComProc
	release        edge.ComProc
	invoke         edge.ComProc
}

// hostDropBridge 是传给 COM 的事件处理器对象。
// 首字段必须是 vtable 指针：COM 从对象指针的第 0 个指针取 vtable，插别的字段在前面会直接崩。
type hostDropBridge struct {
	vtbl    *hostDropVtbl
	onPaths func([]string)
	logf    func(string)
}

var (
	hostDropVtblOnce  *hostDropVtbl
	hostDropKeepalive *hostDropBridge
)

// chromiumOf 从门面里掏出 *edge.Chromium。
// 门面的 browser 字段不导出，只能 reflect+unsafe 取地址再还原成接口值。
func chromiumOf(w webview2.WebView) (ch *edge.Chromium) {
	defer func() {
		if recover() != nil {
			ch = nil
		}
	}()
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return nil
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() || !f.CanAddr() {
		return nil
	}
	iface := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface()
	ch, _ = iface.(*edge.Chromium)
	return ch
}

func buildHostDropVtbl() *hostDropVtbl {
	return &hostDropVtbl{
		queryInterface: edge.NewComProc(func(self, riid, out uintptr) uintptr {
			if out != 0 {
				*(*uintptr)(uintptrToPtr(out)) = self
			}
			return 0 // 只实现 IUnknown，其余一律放行给自己
		}),
		addRef:  edge.NewComProc(func(self uintptr) uintptr { return 2 }),
		release: edge.NewComProc(func(self uintptr) uintptr { return 1 }),
		invoke: edge.NewComProc(func(self, sender, args uintptr) uintptr {
			b := (*hostDropBridge)(uintptrToPtr(self))
			if b == nil || b.onPaths == nil {
				return 0
			}
			paths := additionalObjectPaths(args)
			if len(paths) == 0 {
				return 0
			}
			b.onPaths(paths)
			return 0
		}),
	}
}

// additionalObjectPaths 从 args（ICoreWebView2WebMessageReceivedEventArgs）里把附加对象取出来，
// 每个对象当 ICoreWebView2File 调 GetPath。取不到就返回 nil（普通字符串消息会走到这里）。
func additionalObjectPaths(args uintptr) []string {
	a := uintptrToPtr(args)
	if a == nil {
		return nil
	}
	var args2 unsafe.Pointer
	if hr := comCall(a, 0 /*QueryInterface*/, uintptr(unsafe.Pointer(iidWebMessageReceivedEventArgs2)),
		uintptr(unsafe.Pointer(&args2))); !hrOK(hr) || args2 == nil {
		return nil
	}
	defer comCall(args2, slotRelease)

	var coll unsafe.Pointer
	if hr := comCall(args2, slotGetAdditionalObjects, uintptr(unsafe.Pointer(&coll))); !hrOK(hr) || coll == nil {
		return nil
	}
	defer comCall(coll, slotRelease)

	var n uint32
	if hr := comCall(coll, slotGetCount, uintptr(unsafe.Pointer(&n))); !hrOK(hr) || n == 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := uint32(0); i < n; i++ {
		var unk unsafe.Pointer
		if hr := comCall(coll, slotGetValueAtIndex, uintptr(i), uintptr(unsafe.Pointer(&unk))); !hrOK(hr) || unk == nil {
			continue // null / undefined 传进来就是一个空指针，跳过
		}
		var p *uint16
		hr := comCall(unk, slotGetPath, uintptr(unsafe.Pointer(&p)))
		comCall(unk, slotRelease)
		if !hrOK(hr) || p == nil {
			continue
		}
		out = append(out, windows.UTF16PtrToString(p))
		windows.CoTaskMemFree(unsafe.Pointer(p))
	}
	return out
}

// attachHostDrop 在 UI 线程上注册 WebMessageReceived 处理器。
// 返回 false 表示这条通道不可用（老运行时没有 args2 / 库结构变了），调用方应回落到 JS 就地渲染。
func attachHostDrop(w webview2.WebView, onPaths func([]string), logf func(string)) (ok bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			ok, err = false, errors.New("注册附加对象通道时崩溃")
		}
	}()
	ch := chromiumOf(w)
	if ch == nil {
		return false, errors.New("拿不到 Chromium（库结构变了）")
	}
	v3 := ch.GetICoreWebView2_3()
	if v3 == nil {
		return false, errors.New("QI ICoreWebView2_3 失败")
	}
	if hostDropVtblOnce == nil {
		hostDropVtblOnce = buildHostDropVtbl()
	}
	b := &hostDropBridge{vtbl: hostDropVtblOnce, onPaths: onPaths, logf: logf}
	hostDropKeepalive = b // COM 只 AddRef 不持有 Go 引用，必须自己按着
	hr := comCall(unsafe.Pointer(v3), slotAddWebMessageReceived, uintptr(unsafe.Pointer(b)), 0)
	if !hrOK(hr) {
		return false, errors.New("AddWebMessageReceived 返回 " + itoa(int(hr)))
	}
	return true, nil
}
