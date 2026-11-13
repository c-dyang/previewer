# TODO / 路线图

> **状态：v0.2.0（2026-11-13）。** 下面全部条目为**未排期参考**，不代表正在做。
> 已交付：Flash Player 式外壳（原生标题栏 + 22px 菜单条 + 铺满画布）· 7 类内容路由 + 三条降级链 ·
> 裸开 + 双通路拖拽 + 就地渲染 · `--probe --json` 契约 · 无控制台黑框。
> 验收口径固定为「一条命令 + 一个可数出来的数」。

## 未排期（能力短板）

- [ ] **T0 画面拖拽拿不到路径** — 拖到画面里只有文件名和内容（Chromium 只给 `{name,type,size}`，
  无 `text/uri-list`、无 `File.path`）。就地渲染能用，但定位 / 复制路径 / 换后端都不可用。
  验收：拖任意文件到画面后，`文件 → 在资源管理器中显示` 不再是灰的。
  做法：给 WebView2 控制器设 `AllowExternalDrop=false`（`ICoreWebView2Controller4`），让 drop 落到顶层窗口走 `WM_DROPFILES`。
  ⚠️ 这个接口 `go-webview2` 没暴露、`controller` 字段也不导出 —— 要么给上游提 PR，要么自己 fork 一份 patch。

- [ ] **T1 大文本虚拟滚动** — 现在 4 MiB 以上截断。
  验收：`--probe --json <8GB 日志>` 的 `degraded_reason` 不再是 `text_truncated_at_limit`；
  开窗 < 800 ms，滚动到末尾无卡顿。
  做法：mmap + 行偏移索引（每 64 KB 记一个换行位置）+ 只渲染可视窗口内的行。

- [ ] **T2 外部播放器优先级** — 现在按 mpv → VLC → PotPlayer → ffplay 顺序找。
  验收：`--probe --json` 的 `external_players[0]` 符合本机安装情况；降级能落到 mpv。
  做法：`PickPlayer` 里加一个可配置的偏好表（配置文件或环境变量）。

- [ ] **T3 自己的文件选择器** — 现在 `Ctrl+O` 借 PowerShell WinForms 对话框，约 1 s 起窗延迟。
  验收：`Ctrl+O` 到对话框出现 < 200 ms，且进程树里没有 `powershell.exe`。
  做法：`IFileOpenDialog`（COM），而不是 `GetOpenFileNameW`（结构体布局手写易错）。

- [ ] **T4 md 表格 / 任务列表** — 现在表格按段落原样输出。
  验收：渲染含 3 列表格的 md，窗口内出现 `<table>`（`--debug` + DevTools 验）。

## 未排期（功能扩展）

- [ ] **T5 目录路由** — 传目录时列出子项（列表或缩略图墙），双击再进窗口。
- [ ] **T6 多实例共享用户数据目录的并发验证** — 现在共用 `%LOCALAPPDATA%\previewer\WebView2`，
  没测过 5 个实例同时开关。验收：并发开 5 个不同文件，`--probe` 全部 `present:true` 且无进程残留。
- [ ] **T7 图片自解码兜底** — `.tif/.heic/.psd` 走本进程解码（`golang.org/x/image` + `tiff`），
  而不是交外部播放器。验收：无外部播放器时 `.tif` 仍能显示。
- [ ] **T8 记住窗口尺寸/位置** — 写 `%LOCALAPPDATA%\previewer\window.json`，下次同类型沿用。
- [ ] **T9 SVG 当图片还是当页面** — 现在 `.svg` 当图片走 `<img>`；带脚本的 SVG 需要直通。按内容分流。
- [ ] **T11 `--server` 复用实例** — 宿主池化一个进程（stdin 行协议 `open/close/zoom`），
  把打开延迟从 ~600 ms 压到几十毫秒。与"临时窗口态"信条有冲突，暂不做。

## 已知坑（改代码前先读）

细节与推演见 [`docs/design-notes.md`](docs/design-notes.md)，这里只列清单：

1. `CopyFromScreen` 抓不到 WebView2 画面（永远是黑块），`PrintWindow` 偶发陈旧合成 ⇒ 验证走页面回执，不走截图。
2. `NewWithOptions` 失败时会 `log.Fatal`，没法 recover ⇒ 调用前必须先 `Probe()`。
3. **不要开 DPI 感知**：库不设 `RasterizationScale`，开了会整页放大裁边。
4. 外壳页必须落盘后 `Navigate(file://)`，不能用 `SetHtml`（源是 `about:blank`，读不到 `file://` 子资源）。
5. 自动化键盘要先 `SetFocus` 到 `Chrome_RenderWidgetHostHWND`，或直接 `PostMessage WM_KEYDOWN`。
6. `.pdf` 判据只有文件头 `%PDF-`，后缀不算数。
7. `regexp` 是 RE2，不支持反向引用（`\1` 会 panic 在 `init`）。
8. `gofmt -w` 会改 mtime，之后热重载式的编辑会报 "file changed since it was read"，先重读。
9. `go-webview2` 的 HRESULT 判断是错的（`int64(res) < 0` 判 `uintptr` 里的 `0x8007xxxx`）⇒ 已在 `open()` 加 `recover()`。
10. Session 0（SSH / 服务会话）起不了窗口 ⇒ 开窗前判会话号，exit 8；`--probe` 不受限。
11. `flag` 遇到第一个非选项参数就停 ⇒ 选项必须写在路径前。
12. `go vet` 会抓 `Fprintf` 里的裸 `%`，写 `55%%`。
13. 外壳 JS 语法错 = 全静默故障（窗口正常、交互全死）⇒ `shell_test.go` 用 `node --check` 兜住。
14. `Eval` 只能在 UI 线程调（`RPC_E_WRONG_THREAD` 且库不看返回值）⇒ 一律 `w.Dispatch`。
15. 二进制保持 CUI 子系统（`Subsystem=3`），否则 `os.Stdout` 失效、`--probe --json` 废掉；
    黑框靠 `GetConsoleProcessList` 判"控制台只挂着自己"才藏。`-H windowsgui` 已否决。

## 待决

- 要不要接到右键菜单（`HKCU\Software\Classes\*\shell`）？
- 要不要和文件标签系统联动：预览窗口里直接读写标签？
