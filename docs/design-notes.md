# 设计笔记：把 WebView2 当渲染器用时踩的坑

这个程序的技术核心不是"打开文件"，而是"用 Go 拉起一个 WebView2 窗口、往里塞内容、并且让失败可诊断"。
下面每一条都是真机上撞出来的，按症状 → 根因 → 处置写，改代码前建议先扫一遍。

## 1. `go-webview2` 的 HRESULT 判断是错的，失败会变成空指针 panic

绑定库里判失败的写法是 `int64(res) < 0`，而 `res` 的类型是 `uintptr` ——
COM 的失败码 `0x8007xxxx` 装在 `uintptr` 里是**正数**，判不出来，于是代码继续往下走，
下一行 `controller.vtbl` 就是空指针解引用。

处置：开窗前先 `Probe()` 判 WebView2 是否 `present`（缺运行时直接 exit 3），
`open()` 里加 `defer recover()` 把任何 panic 转成一句可读原因 + exit 6。
调用方永远不该看到 Go 堆栈。升级这个库要重新验这条。

## 2. Session 0 里没有交互桌面，WebView2 建不出 controller

经 SSH / 服务 / CI 起的进程在 Session 0，`CreateCoreWebView2Controller` 必然失败，
在修好错误处理之前这里就是上面那个 panic。处置：开窗前判会话号，不是交互会话直接 exit 8 并说明原因。

推论（也是这个程序最有用的一个特性）：**`--probe` 必须能在 Session 0 跑**。
能力探测和路由验证全部走 `--probe --json`，不需要窗口，于是远端 / 无头环境也能验收。

要在远端做窗口级验证，只能走交互式计划任务（`schtasks /ru <用户> /it` + 一个落日志的脚本）。

## 3. 不要给这个进程开 DPI 感知

踩过一次很典型的连环坑：给进程设 `SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2)` 之后，
页面自报 `innerWidth=1676 dpr=1.25 client=1675` —— 视口按物理像素给，却按 1.25 倍栅格化，
内容整页放大 25%、右侧被裁（状态标签看不见、居中文字偏右）。

根因不是 DPI 感知本身，而是**绑定的库从不调 `PutRasterizationScale`**，把它留给宿主。
结论：在这个程序里宁可不开 DPI 感知（让系统做虚拟化），也不要开一半。

## 4. `Eval` 必须在 UI 线程调，否则静默无效

`CoreWebView2` 是单元线程对象。从别的 goroutine 直接调 `Eval` 返回 `RPC_E_WRONG_THREAD`，
而库不看返回值 —— 现象是"注入什么都没发生"，不报错、不崩，最难查。

处置：一律 `w.Dispatch(func(){ w.Eval(script) })`。

排查手法值得记一下：写一个只做 `Eval("document.body.style.background='red'")` + 一个跳秒时钟的
最小程序，先证明 `Eval` 本身能用，再去怀疑调用线程 —— 否则会在"是不是 blob origin 的问题 /
是不是时机不对"上耗掉半天。

## 5. 外壳页必须落盘再 `Navigate(file://)`，不能用 `SetHtml`

`SetHtml` 的源是 `about:blank`，读不到 `file://` 子资源，`<img>` / `<video>` 会全黑。
所以外壳 HTML 写到临时文件再 `Navigate`。

## 6. 外壳 JS 一个括号错，全部交互一起静默失效

症状极具欺骗性：窗口照画、标题照对、内容照显示，只是菜单点不开、拖拽没反应、换内容不生效。
因为整段 `<script>` 语法错就整体不执行，而窗口和页面结构是 Go 生成的，不受影响。

处置：`shell_test.go` 里用 `node --check` 校验拼出来的脚本（没装 node 就 skip）。
改 JS 之后必须跑 `go test`。

## 7. 截图不能当渲染判据

- `CopyFromScreen` 抓 WebView2 永远是黑块（DirectComposition，不在 GDI 表面上）。
- `PrintWindow(PW_RENDERFULLCONTENT=2)` 能抓到，但**偶发陈旧合成** —— 会看到上一帧的窗口叠在新内容上，
  一度误判成"blob 图片不显示"。

处置：功能真相取**页面自报回执**。内容替换成功后页面回传 `pvReplace 落地 <kind>:<class>:<len>`，
图片加载后回传 `图片加载 ok:<W>x<H>`，这些落在 `%LOCALAPPDATA%\previewer\drop.log`。
自报绿色不算数，但自报的**结构性回执**（类型、长度、像素尺寸）是可数的。

## 8. 画面拖拽拿不到路径，这是 Chromium 的边界

`dataTransfer` 只给 `{name, type, size}`，`File.path` 恒空（Electron 私有扩展），也没有 `text/uri-list`。
所以"拖进画面"和"拖到标题栏"是两条不同能力的通路（`WM_DROPFILES` 才有完整路径），必须显式区分，
并给就地渲染打上 `drop_without_path`，而不是假装一切正常。

## 9. 其它零碎

- **`regexp` 是 RE2，不支持反向引用**：`\1` 写在 `MustCompile` 里会直接 panic 在 `init`，连 `main` 都进不去。
  分隔线那种 `---` / `***` / `___` 用三选一的 `{3,}` 分支写。
- **`go vet` 会抓 `Fprintf` 里的裸 `%`**：usage 里写 `55%` 输出会变成 `55%!）(MISSING)`，要写 `55%%`。
- **`flag` 遇到第一个非选项参数就停**：选项必须写在文件路径之前。
- **自动化拖拽要注意坐标虚拟化**：不设 DPI 感知的测试进程里，`SetCursorPos` / `GetWindowRect` 用的是虚拟化坐标，
  而 UI 自动化返回的是物理像素，两者混用会让拖拽"看起来点了但没命中"。
  测试工具自己要先 `SetProcessDpiAwarenessContext(-4)`。
- **键盘消息要落到子窗口**：WebView2 的输入目标是 `Chrome_RenderWidgetHostHWND`，
  测试脚本要先 `SetFocus` 那个子窗口，或者直接 `PostMessage WM_KEYDOWN`。
- **PDF 判据只有文件头**：`.pdf` 后缀不代表内容，先看是不是 `%PDF-`。
  一份名为 datasheet 的"PDF"其实是 HTML，害得白查了一轮 viewer。
- **`NewWithOptions` 内部会 pump 消息直到 controller 就绪**，所以返回后可以立刻 `Navigate`；
  但它失败时会 `log.Fatal`，没法 recover —— 这是为什么必须在调用前先 `Probe()`。
