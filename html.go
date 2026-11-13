package main

import (
	"encoding/json"
	"html"
	"net/url"
	"path/filepath"
	"strings"
)

// fileURL 把本地路径转成 file:// URL（中文路径必须百分号编码，否则 Navigate 会挂）。
func fileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

type menuItem struct {
	ID    string
	Label string
	Key   string
	Sep   bool
	Dis   bool
}

type menuDef struct {
	Name  string
	Accel string
	Items []menuItem
}

// menusFor 按内容类型生成菜单。控制(C) 只在有媒体时可用——不可用的项置灰，不隐藏，
// 这样菜单条宽度恒定，视觉上就是 Flash Player 那种"永远那四个菜单"。
func menusFor(p *Plan) []menuDef {
	hasMedia := p.Kind == KindVideo || p.Kind == KindAudio
	hasView := p.Kind == KindAudio || p.Kind == KindBinary

	return []menuDef{
		{Name: "文件", Accel: "F", Items: []menuItem{
			{ID: "open", Label: "打开…", Key: "Ctrl+O"},
			{ID: "reveal", Label: "在资源管理器中显示", Key: "Ctrl+E"},
			{ID: "copy", Label: "复制完整路径"},
			{Sep: true},
			{ID: "external", Label: "用默认程序打开", Key: "Ctrl+Enter"},
			{Sep: true},
			{ID: "quit", Label: "退出", Key: "Esc"},
		}},
		{Name: "查看", Accel: "V", Items: []menuItem{
			{ID: "fullscreen", Label: "全屏", Key: "F11"},
			{Sep: true},
			{ID: "zoomin", Label: "放大", Key: "Ctrl+=", Dis: hasMedia},
			{ID: "zoomout", Label: "缩小", Key: "Ctrl+-", Dis: hasMedia},
			{ID: "zoomreset", Label: "实际大小", Key: "Ctrl+0", Dis: hasView},
			{Sep: true},
			{ID: "info", Label: "文件信息", Key: "Ctrl+I"},
		}},
		{Name: "控制", Accel: "C", Items: []menuItem{
			{ID: "play", Label: "播放 / 暂停", Key: "Space", Dis: !hasMedia},
			{ID: "rewind", Label: "后退 5 秒", Key: "←", Dis: !hasMedia},
			{ID: "forward", Label: "前进 5 秒", Key: "→", Dis: !hasMedia},
			{Sep: true},
			{ID: "mute", Label: "静音", Key: "M", Dis: !hasMedia},
			{ID: "volup", Label: "音量 +", Key: "↑", Dis: !hasMedia},
			{ID: "voldown", Label: "音量 -", Key: "↓", Dis: !hasMedia},
		}},
		{Name: "帮助", Accel: "H", Items: []menuItem{
			{ID: "about", Label: "关于 previewer"},
		}},
	}
}

func escape(s string) string { return html.EscapeString(s) }

// srcOf 给渲染标签用的 URL：拖拽就地渲染时是 JS 给的 blob:，否则是 file://。
func srcOf(p *Plan) string {
	if p.BlobURL != "" {
		return p.BlobURL
	}
	return fileURL(p.Path)
}

// statusHTML 是菜单条右侧那串「名字 · 大小 [· 降级原因]」。
func statusHTML(p *Plan) string {
	if p.Kind == KindEmpty {
		return escape(dropTitle) + ` · 拖入文件或 Ctrl+O`
	}
	s := escape(p.Name)
	if p.Size > 0 {
		s += " · " + humanSize(p.Size)
	}
	if d := DegradedLabel(p); d != "" {
		s += ` · <b>` + escape(d) + `</b>`
	}
	return s
}

func menuBarHTML(menus []menuDef, p *Plan) string {
	var sb strings.Builder
	for _, m := range menus {
		sb.WriteString(`<div class="m" data-menu="` + m.Name + `">` + escape(m.Name) + `(` + m.Accel + `)</div>`)
	}
	sb.WriteString(`<div class="sp"></div>`)
	sb.WriteString(`<div class="st">` + statusHTML(p) + `</div>`)
	return sb.String()
}

func popupsHTML(menus []menuDef) string {
	var sb strings.Builder
	for _, m := range menus {
		sb.WriteString(`<div class="pop" data-for="` + m.Name + `">`)
		for _, it := range m.Items {
			if it.Sep {
				sb.WriteString(`<div class="sep"></div>`)
				continue
			}
			cls := "i"
			if it.Dis {
				cls += " dis"
			}
			sb.WriteString(`<div class="` + cls + `" data-id="` + it.ID + `">` + escape(it.Label))
			if it.Key != "" {
				sb.WriteString(`<span class="k">` + escape(it.Key) + `</span>`)
			}
			sb.WriteString(`</div>`)
		}
		sb.WriteString(`</div>`)
	}
	return sb.String()
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return itoa(int(n)) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	whole := n / div
	frac := (n * 10 / div) % 10
	if frac == 0 {
		return itoa(int(whole)) + " " + string("KMGT"[exp]) + "B"
	}
	return itoa(int(whole)) + "." + itoa(int(frac)) + " " + string("KMGT"[exp]) + "B"
}

// contentFor 造内容区。external / direct 两类不走这里。
func contentFor(p *Plan) string {
	switch p.Kind {
	case KindEmpty:
		return `<div class="drop">` +
			`<div class="di">把文件拖进来</div>` +
			`<div class="ds">或 文件 → 打开…（Ctrl+O） · Esc 关闭</div>` +
			`</div>`
	case KindMarkdown:
		return `<article class="md">` + renderMarkdown(p.Text) + `</article>`
	case KindImage:
		return `<div class="imgwrap"><img src="` + srcOf(p) + `" alt="` + escape(p.Name) + `"></div>`
	case KindPDF:
		// 套 iframe：拿 WebView2 内置 PDF viewer，但保留统一的菜单条
		return `<iframe class="pdframe" src="` + srcOf(p) + `"></iframe>`
	case KindHTML:
		return `<iframe class="pdframe" src="` + srcOf(p) + `"></iframe>`
	case KindVideo:
		return `<video id="v" src="` + srcOf(p) + `" preload="auto" autoplay></video>` + controlsHTML()
	case KindAudio:
		return `<div class="audiowrap"><div class="audioname">` + escape(p.Name) + `</div>` +
			`<audio id="v" src="` + srcOf(p) + `" preload="auto" autoplay></audio></div>` + controlsHTML()
	case KindText:
		return `<pre class="txt">` + escape(p.Text) + `</pre>`
	case KindBinary:
		if p.Text == "" {
			return `<div class="hint"><div class="hi">这个文件需要换后端才能看</div>` +
				`<div class="hs">拖到画面里拿不到路径，起不了外部播放器 / 解码器。<br>` +
				`拖到标题栏，或用 文件 → 打开…（Ctrl+O），就能拿到完整路径走完整路由。</div></div>`
		}
		return `<pre class="hex">` + escape(p.Text) + `</pre>`
	}
	return ""
}

func controlsHTML() string {
	return `<div id="mbar">` +
		`<div id="play"></div>` +
		`<span id="tb">0:00 / 0:00</span>` +
		`<div id="seek"><div class="buf"></div><div class="px"></div></div>` +
		`</div>` +
		`<div id="err"><div class="box"><div class="t">Chromium 解不了这个文件</div>` +
		`<div class="d" id="errmsg"></div>` +
		`<button onclick="pvSend('external')">用外部程序打开</button></div></div>`
}

const shellCSS = `
*{box-sizing:border-box}
html,body{margin:0;padding:0;height:100%;overflow:hidden;background:#fff;
  font-family:"Segoe UI","Microsoft YaHei UI","Microsoft YaHei","宋体",sans-serif;font-size:12px;color:#222}
#bar{position:fixed;top:0;left:0;right:0;height:22px;display:flex;align-items:stretch;
  background:#f0f0f0;border-bottom:1px solid #a0a0a0;user-select:none;z-index:30}
#bar .m{padding:0 9px;line-height:21px;cursor:default;white-space:nowrap}
#bar .m:hover,#bar .m.on{background:#cce4f7}
#bar .sp{flex:1}
#bar .st{padding:0 9px;line-height:21px;color:#777;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:60%}
#bar .st b{font-weight:600;color:#b06a00}
.pop{position:fixed;z-index:40;display:none;background:#f7f7f7;border:1px solid #a0a0a0;
  box-shadow:2px 2px 6px rgba(0,0,0,.25);padding:2px 0;min-width:210px}
.pop.show{display:block}
.pop .i{position:relative;padding:3px 24px 3px 24px;line-height:18px;cursor:default;white-space:nowrap}
.pop .i:hover{background:#cce4f7}
.pop .i.dis{color:#aaa}
.pop .i.dis:hover{background:transparent}
.pop .i .k{position:absolute;right:10px;color:#888}
.pop .sep{height:1px;margin:3px 6px;background:#dcdcdc}
#cv{position:fixed;top:22px;left:0;right:0;bottom:0;overflow:auto}
.k-image{display:flex;align-items:center;justify-content:center;overflow:hidden;
  background-color:#2f2f2f;
  background-image:linear-gradient(45deg,#3a3a3a 25%,transparent 25%),linear-gradient(-45deg,#3a3a3a 25%,transparent 25%),
    linear-gradient(45deg,transparent 75%,#3a3a3a 75%),linear-gradient(-45deg,transparent 75%,#3a3a3a 75%);
  background-size:16px 16px;background-position:0 0,0 8px,8px -8px,-8px 0}
.imgwrap{width:100%;height:100%;display:flex;align-items:center;justify-content:center}
.imgwrap img{max-width:100%;max-height:100%;object-fit:contain;display:block}
.k-video{background:#000;overflow:hidden}
.k-video video{width:100%;height:100%;object-fit:contain;display:block;background:#000}
.k-audio{background:#141414}
.audiowrap{height:100%;display:flex;align-items:center;justify-content:center;color:#ddd}
.audioname{font-size:15px;letter-spacing:.5px;opacity:.9}
.k-pdf{background:#525659;overflow:hidden}
.pdframe{display:block;width:100%;height:100%;border:0}
.k-empty{display:flex;align-items:center;justify-content:center;background:#fbfbfb;
  transition:background .12s,box-shadow .12s;box-shadow:inset 0 0 0 0 #2d6cdf}
body.dragging .k-empty{background:#eef4ff;box-shadow:inset 0 0 0 3px #2d6cdf}
body.dragging #cv::after{content:"松开即打开";position:fixed;left:50%;bottom:40px;transform:translateX(-50%);
  background:#2d6cdf;color:#fff;padding:6px 14px;border-radius:3px;font-size:12px}
.drop{text-align:center;color:#666;user-select:none;pointer-events:none}
.drop .di{font-size:19px;color:#3a3a3a;margin-bottom:10px;letter-spacing:.5px}
.drop .ds{font-size:12px;color:#8a8a8a}
.k-binary{background:#fbfbfb}
.hint{height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center;text-align:center;gap:10px}
.hint .hi{font-size:16px;color:#3a3a3a}
.hint .hs{font-size:12px;color:#8a8a8a;line-height:1.8}
.k-text{background:#fff}
pre.txt{margin:0;padding:14px 18px;font:12px/1.55 "Cascadia Mono",Consolas,"Microsoft YaHei Mono",monospace;
  white-space:pre;tab-size:4;color:#222}
pre.hex{margin:0;padding:14px 18px;font:12px/1.5 "Cascadia Mono",Consolas,monospace;white-space:pre;color:#333}
.k-markdown{background:#fff}
.md{max-width:920px;margin:0 auto;padding:26px 34px 80px;font-size:14px;line-height:1.72;color:#1a1a1a}
.md h1,.md h2,.md h3,.md h4,.md h5,.md h6{margin:1.5em 0 .6em;line-height:1.35;font-weight:600}
.md h1{font-size:1.75em;border-bottom:1px solid #e2e2e2;padding-bottom:.3em}
.md h2{font-size:1.4em;border-bottom:1px solid #ececec;padding-bottom:.25em}
.md h3{font-size:1.2em}
.md h4{font-size:1.05em}
.md p{margin:.75em 0}
.md ul,.md ol{margin:.6em 0;padding-left:1.9em}
.md li{margin:.22em 0}
.md blockquote{margin:.8em 0;padding:.4em 1em;border-left:3px solid #d0d0d0;color:#555;background:#fafafa}
.md hr{border:0;border-top:1px solid #e2e2e2;margin:1.6em 0}
.md code{font-family:"Cascadia Mono",Consolas,monospace;font-size:.9em;background:#f2f2f2;
  padding:.12em .35em;border-radius:3px;color:#b03060}
.md pre{background:#f6f6f6;border:1px solid #e6e6e6;border-radius:4px;padding:10px 12px;overflow:auto;margin:.9em 0}
.md pre code{background:none;padding:0;color:#24292e;font-size:12px;line-height:1.55}
.md a{color:#0b62c4;text-decoration:none}
.md a:hover{text-decoration:underline}
.md img{max-width:100%}
#mbar{position:fixed;left:0;right:0;bottom:0;height:34px;display:flex;align-items:center;gap:10px;
  padding:0 12px;background:rgba(24,24,24,.88);color:#eee;font-size:11px;z-index:35;
  opacity:0;transition:opacity .18s;font-variant-numeric:tabular-nums}
body.ui #mbar{opacity:1}
#play{width:22px;height:22px;position:relative;cursor:pointer;flex:0 0 auto}
#play::before{content:"";position:absolute;left:6px;top:4px;border-left:9px solid #eee;
  border-top:7px solid transparent;border-bottom:7px solid transparent}
#play.on::before{left:5px;top:4px;border:0;width:4px;height:14px;background:#eee;box-shadow:8px 0 0 #eee}
#tb{flex:0 0 auto;opacity:.85;min-width:98px}
#seek{flex:1;height:4px;background:#555;border-radius:2px;position:relative;cursor:pointer}
#seek .buf{position:absolute;top:0;bottom:0;left:0;width:0;background:#8a8a8a;border-radius:2px}
#seek .px{position:absolute;top:0;bottom:0;left:0;width:0;background:#4aa3ff;border-radius:2px}
#err{position:fixed;inset:22px 0 0 0;display:none;align-items:center;justify-content:center;z-index:36;background:#111}
#err.show{display:flex}
#err .box{text-align:center;color:#ddd;font-size:13px}
#err .t{font-size:15px;margin-bottom:8px}
#err .d{color:#999;margin-bottom:16px;max-width:520px;line-height:1.6}
#err button{background:#2d6cdf;color:#fff;border:0;border-radius:3px;padding:7px 16px;font-size:12px;cursor:pointer}
`

func shellJS(p *Plan) string {
	info := map[string]any{
		"kind":     string(p.Kind),
		"name":     p.Name,
		"path":     p.Path,
		"size":     p.Size,
		"degraded": p.Degraded,
		"notes":    p.Notes,
	}
	meta, _ := json.Marshal(info)

	return `
window.__pvShell = true;   // 让 globalJS 的 Esc/F11 兜底让位，避免重复触发
let INFO = ` + string(meta) + `;
let mediaEl = null;        // 当前 <video>/<audio>，拖拽换页后会被重建，所以不当常量用

function pvSend(id){ try{ if (typeof pvAct === 'function') pvAct(id); }catch(e){} }
// 回执：宿主有没有把「画面拖拽拿路径」那条通道打通（Go 侧注册成功后注入 __pvCanDropPath）
pvSend('hostdrop:' + (window.__pvCanDropPath ? 'ready' : 'off'));
function fmt(t){
  if (!isFinite(t) || t < 0) t = 0;
  const m = Math.floor(t/60), s = Math.floor(t%60);
  return m + ':' + (s<10?'0':'') + s;
}

/* ---------- 菜单 ---------- */
const menuEls = Array.from(document.querySelectorAll('#bar .m'));
const pops = Array.from(document.querySelectorAll('.pop'));
function closeMenus(){
  pops.forEach(x=>x.classList.remove('show'));
  menuEls.forEach(x=>x.classList.remove('on'));
}
function openMenu(m){
  const name = m.dataset.menu;
  const pop = document.querySelector('.pop[data-for="'+name+'"]');
  if (!pop) return;
  const already = pop.classList.contains('show');
  closeMenus();
  if (already) return;
  m.classList.add('on');
  pop.classList.add('show');
  pop.style.left = Math.min(m.offsetLeft, window.innerWidth - pop.offsetWidth - 4) + 'px';
  pop.style.top = '22px';
}
menuEls.forEach(m=>{
  m.addEventListener('click', e=>{ e.stopPropagation(); openMenu(m); });
  m.addEventListener('mouseenter', ()=>{
    if (document.querySelector('.pop.show') && !m.classList.contains('on')) openMenu(m);
  });
});
document.addEventListener('click', closeMenus);
document.addEventListener('contextmenu', e=>e.preventDefault());

document.querySelectorAll('.pop .i').forEach(it=>{
  it.addEventListener('click', e=>{
    e.stopPropagation();
    if (it.classList.contains('dis')) { closeMenus(); return; }
    closeMenus();
    pvSend(it.dataset.id);
  });
});

/* ---------- 缩放（纯前端，不惊动 Go） ---------- */
let zoom = 1;
function applyZoom(z){
  zoom = Math.min(4, Math.max(0.25, z));
  document.getElementById('cv').style.zoom = zoom;
  pvSend('zoom-changed:' + zoom);
}

/* ---------- 媒体 ----------
   setupMedia 必须可重复调用：拖入新文件时内容区整块换掉，旧的 <video> 已经不在 DOM 里了，
   原来那种"加载时抓一次 #v 存成常量"的写法会指向僵尸节点。 */
function togglePlay(){ if (!mediaEl) return; mediaEl.paused ? mediaEl.play() : mediaEl.pause(); }

function tick(){
  const v = mediaEl;
  if (!v) return;
  const d = v.duration || 0, c = v.currentTime || 0;
  const bar = document.getElementById('seek');
  if (bar) bar.querySelector('.px').style.width = (d ? (c/d*100) : 0) + '%';
  const tb = document.getElementById('tb');
  if (tb) tb.textContent = fmt(c) + ' / ' + fmt(d);
  const pl = document.getElementById('play');
  if (pl) pl.classList.toggle('on', !v.paused);
}

function setupMedia(){
  const v = document.getElementById('v');
  mediaEl = v || null;
  if (!v) return;

  v.addEventListener('timeupdate', tick);
  v.addEventListener('durationchange', tick);
  v.addEventListener('progress', ()=>{
    if (!v.buffered.length) return;
    const d = v.duration || 1;
    const bar = document.getElementById('seek');
    if (bar) bar.querySelector('.buf').style.width = (v.buffered.end(v.buffered.length-1)/d*100) + '%';
  });
  v.addEventListener('play', tick);
  v.addEventListener('pause', tick);
  v.addEventListener('click', togglePlay);
  v.addEventListener('dblclick', ()=>pvSend('fullscreen'));
  v.addEventListener('error', ()=>{
    const box = document.getElementById('err');
    if (!box) return;
    box.classList.add('show');
    const msg = document.getElementById('errmsg');
    if (msg) msg.textContent =
      '容器 .' + (String(INFO.name).split('.').pop() || '?') + ' 或其中的编码不在 Chromium 内置解码器范围内。' +
      '（运行时兜底：probe 阶段没识别出编码时会走到这里）';
  });
  const box = document.getElementById('seek');
  if (box) box.addEventListener('click', e=>{
    const r = box.getBoundingClientRect();
    if (v.duration) v.currentTime = Math.max(0, Math.min(1, (e.clientX - r.left)/r.width)) * v.duration;
  });
  const pl = document.getElementById('play');
  if (pl) pl.addEventListener('click', e=>{ e.stopPropagation(); togglePlay(); });

  // autoplay 被拦时不报错，只是静默 —— 补一次显式 play
  if (v.play) v.play().catch(()=>{});
  tick();
}
setupMedia();
setInterval(tick, 250);

/* ---------- 就地换页：Go 侧算好内容，这里只负责换上 ---------- */
window.pvReplace = function(html, kind, statusHTML, pathless, hasMedia, info){
  const cv = document.getElementById('cv');
  if (!cv) return false;
  if (info) INFO = info;
  cv.className = 'k-' + kind;
  cv.innerHTML = html;
  cv.scrollTop = 0;
  const st = document.querySelector('#bar .st');
  if (st && statusHTML) st.innerHTML = statusHTML;
  if (pathless) {
    // 没有路径 ⇒ 定位/复制路径/交给默认程序 都无从谈起，置灰而不是点了没反应
    ['reveal','copy','external'].forEach(id=>{
      document.querySelectorAll('.pop .i[data-id="'+id+'"]').forEach(el=>el.classList.add('dis'));
    });
  }
  // 控制菜单是渲染时就定死置灰的，换页后要按新类型重算
  document.querySelectorAll('.pop[data-for="控制"] .i').forEach(el=>{
    el.classList.toggle('dis', !hasMedia);
  });
  const drop = document.querySelector('.drop');
  if (drop) drop.remove();
  setupMedia();
  // 回执给 Go 侧落日志：截图对 WebView2 不可靠，这条才是功能真相
  pvSend('replace-done:' + kind + ':' + cv.className + ':' + cv.innerHTML.length);
  const img = cv.querySelector('img');
  if (img) {
    if (img.complete && img.naturalWidth) {
      pvSend('img-probe:ok:' + img.naturalWidth + 'x' + img.naturalHeight);
    } else {
      img.addEventListener('load', ()=>pvSend('img-probe:ok:' + img.naturalWidth + 'x' + img.naturalHeight));
      img.addEventListener('error', ()=>pvSend('img-probe:error:' + (img.src || '').slice(0, 40)));
    }
  }
  return true;
};

/* 底部控制条自动隐藏 */
let uiTimer = null;
function poke(){
  document.body.classList.add('ui');
  clearTimeout(uiTimer);
  uiTimer = setTimeout(()=>document.body.classList.remove('ui'), 2200);
}
document.addEventListener('mousemove', poke);
poke();

/* ---------- 拖放：把文件从资源管理器拖进来 ---------- */
function collectDrop(dt){
  const out = {types: Array.from(dt.types || []), data: {}, files: []};
  for (const t of out.types) {
    try { out.data[t] = dt.getData(t); } catch(e) { out.data[t] = ''; }
  }
  for (const f of (dt.files || [])) {
    // f.path 是 Electron 私有扩展，Chromium/WebView2 里恒为空 —— 仍然带上，
    // Go 侧会按「有就用、没有再退 text/uri-list」的顺序挑。
    out.files.push({name: f.name, type: f.type, size: f.size, path: (f.path || '')});
  }
  return out;
}
let dragDepth = 0;
document.addEventListener('dragenter', e=>{
  e.preventDefault(); dragDepth++;
  document.body.classList.add('dragging');
});
document.addEventListener('dragover', e=>{
  e.preventDefault();
  try { e.dataTransfer.dropEffect = 'copy'; } catch(_){}
});
document.addEventListener('dragleave', ()=>{
  if (--dragDepth <= 0) { dragDepth = 0; document.body.classList.remove('dragging'); }
});
document.addEventListener('drop', e=>{
  e.preventDefault(); dragDepth = 0;
  document.body.classList.remove('dragging');
  const dt = e.dataTransfer;
  const files = dt.files ? Array.prototype.slice.call(dt.files) : [];
  // 宿主注册了 WebMessageReceived + additional objects 时，把 File 原样交回去，
  // 由 Go 拿绝对路径（这台机器上 __pvCanDropPath 由 Go 在开窗时注入）。
  // 拿不到路径的老运行时仍走下面「名字+内容」的就地渲染。
  if (files.length && window.__pvCanDropPath && window.chrome && chrome.webview &&
      typeof chrome.webview.postMessageWithAdditionalObjects === 'function') {
    try {
      // 交给宿主换内容；万一宿主没接住（老运行时 / 取消），700ms 后自己兜底渲染，
      // 免得拖完一个文件窗口还是空的。
      window.__pvHostDropPending = true;
      setTimeout(function(){
        if (window.__pvHostDropPending && files[0]) {
          window.__pvHostDropPending = false;
          sendDroppedFile(files[0]);
        }
      }, 700);
      chrome.webview.postMessageWithAdditionalObjects(window.__pvDropMsg || 'pv-drop', files);
      return;
    } catch (_) { window.__pvHostDropPending = false; /* 落到旧通路 */ }
  }
  const f = files[0] || null;
  if (!f) { pvSend('drop:' + JSON.stringify(collectDrop(dt))); return; }
  sendDroppedFile(f);
});

function sendDroppedFile(f){
  const ext = (f.name.split('.').pop() || '').toLowerCase();
  // Go 的 extText/extMarkdown 是同一份表的镜像；文本类走"读字节喂给 Go"，
  // 媒体类走"建 blob URL 让 Chromium 自己流式读盘"，都不需要路径。
  const TEXTY = new Set(('txt log nfo csv tsv json jsonl xml yaml yml toml ini inf cfg conf properties reg ' +
    'bat cmd ps1 psm1 py go c h cc cpp hpp cs java kt js mjs ts tsx jsx css scss less sql sh bash zsh rs php rb ' +
    'lua pl r vue svelte diff patch srt ass vtt lrc tex cmake mk ahk vbs awk asm s dts ld map sum mod lock ' +
    'plist manifest service md markdown mdown mkd mkdn').split(' '));
  const limit = 4 * 1024 * 1024;
  const common = {name: f.name, size: f.size, type: f.type, b64: '', url: '', truncated: false};

  if (TEXTY.has(ext) || !ext || (f.type && f.type.indexOf('text/') === 0)) {
    // 传原始字节而不是 readAsText：readAsText 恒按 UTF-8 解，GBK 日志会变乱码。
    // 解码交给 Go 的 decodeBytes（BOM → UTF-8 校验 → GBK 兜底）。
    const blob = (f.size > limit) ? f.slice(0, limit) : f;
    const fr = new FileReader();
    fr.onload = () => {
      const bytes = new Uint8Array(fr.result || new ArrayBuffer(0));
      let bin = '';
      const CH = 0x8000;
      for (let i = 0; i < bytes.length; i += CH) {
        bin += String.fromCharCode.apply(null, bytes.subarray(i, i + CH));
      }
      common.b64 = btoa(bin);
      common.truncated = f.size > limit;
      pvSend('dropblob:' + JSON.stringify(common));
    };
    fr.onerror = () => pvSend('dropblob:' + JSON.stringify(common));
    fr.readAsArrayBuffer(blob);
    return;
  }
  try { common.url = URL.createObjectURL(f); } catch (_) {}
  pvSend('dropblob:' + JSON.stringify(common));
}

/* ---------- 键盘 ---------- */
document.addEventListener('keydown', e=>{
  const k = e.key;
  if (e.ctrlKey) {
    if (k === 'o') { e.preventDefault(); pvSend('open'); return; }
    if (k === 'e') { e.preventDefault(); pvSend('reveal'); return; }
    if (k === 'i') { e.preventDefault(); pvSend('info'); return; }
    if (k === 'Enter') { e.preventDefault(); pvSend('external'); return; }
    if (k === '=' || k === '+') { e.preventDefault(); applyZoom(zoom*1.1); return; }
    if (k === '-') { e.preventDefault(); applyZoom(zoom/1.1); return; }
    if (k === '0') { e.preventDefault(); applyZoom(1); return; }
    return;
  }
  if (e.altKey) {
    const map = {'f':'文件','v':'查看','c':'控制','h':'帮助'};
    const name = map[k.toLowerCase()];
    if (name) {
      e.preventDefault();
      const el = document.querySelector('#bar .m[data-menu="'+name+'"]');
      if (el) openMenu(el);
    }
    return;
  }
  if (k === 'Escape') {
    e.preventDefault();
    // 菜单开着就先收菜单，再按一次才关窗口
    if (document.querySelector('.pop.show')) { closeMenus(); return; }
    pvSend('quit'); return;
  }
  if (k === 'F11' || k === 'f') { e.preventDefault(); pvSend('fullscreen'); return; }

  if (mediaEl) {
    const v = mediaEl;
    if (k === ' ') { e.preventDefault(); togglePlay(); return; }
    if (k === 'ArrowRight') { e.preventDefault(); v.currentTime = Math.min(v.duration||0, v.currentTime + 5); return; }
    if (k === 'ArrowLeft') { e.preventDefault(); v.currentTime = Math.max(0, v.currentTime - 5); return; }
    if (k === 'ArrowUp') { e.preventDefault(); v.volume = Math.min(1, v.volume + 0.05); poke(); return; }
    if (k === 'ArrowDown') { e.preventDefault(); v.volume = Math.max(0, v.volume - 0.05); poke(); return; }
    if (k === 'm' || k === 'M') { e.preventDefault(); v.muted = !v.muted; return; }
  }
});

/* 供 Go 侧回灌命令 */
window.pvDo = function(id){
  const v = mediaEl;
  switch (id) {
    case 'play': togglePlay(); break;
    case 'rewind': if (v) v.currentTime = Math.max(0, v.currentTime - 5); break;
    case 'forward': if (v) v.currentTime = Math.min(v.duration||0, v.currentTime + 5); break;
    case 'mute': if (v) v.muted = !v.muted; break;
    case 'volup': if (v) v.volume = Math.min(1, v.volume + 0.1); break;
    case 'voldown': if (v) v.volume = Math.max(0, v.volume - 0.1); break;
    case 'zoomin': applyZoom(zoom*1.1); break;
    case 'zoomout': applyZoom(zoom/1.1); break;
    case 'zoomreset': applyZoom(1); break;
  }
  return true;
};
`
}

// buildShell 造完整外壳页：22px 菜单条 + 铺满的内容区，没有工具栏、没有侧栏。
func buildShell(p *Plan) string {
	menus := menusFor(p)
	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<title>` + escape(p.Name) + `</title>
<style>` + shellCSS + `</style></head><body>
<div id="bar">` + menuBarHTML(menus, p) + `</div>
<div id="cv" class="k-` + string(p.Kind) + `">` + contentFor(p) + `</div>
` + popupsHTML(menus) + `
<script>` + shellJS(p) + `</script>
</body></html>`)
	return sb.String()
}
