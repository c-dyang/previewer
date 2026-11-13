package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// WebView2Info 描述“这台机器上 WebView2 到底能不能用”，probe 全靠它。
type WebView2Info struct {
	Present    bool            `json:"present"`
	Version    string          `json:"version,omitempty"`
	RuntimeDir string          `json:"runtime_dir,omitempty"`
	Source     string          `json:"source,omitempty"`
	Media      map[string]bool `json:"media_support"`
	Notes      []string        `json:"notes,omitempty"`
}

// Player 是一个可转交的外部后端。
type Player struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Video bool   `json:"video"`
	Audio bool   `json:"audio"`
	Image bool   `json:"image"`
}

// Backends 是本机能做事的全部工具集合。路由判定的"后端可用性"这一维就来自它。
type Backends struct {
	WebView2 WebView2Info   `json:"webview2"`
	Players  []Player       `json:"external_players"`
	OS       string         `json:"os"`
	Arch     string         `json:"arch"`
	Internal map[string]any `json:"internal_capabilities"`
}

const wv2ClientGUID = `{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// Probe 无窗口、无副作用地摸一遍能力面。--probe 走这条路。
func Probe() *Backends {
	b := &Backends{
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
		Internal: map[string]any{
			"markdown":     true,
			"text":         true,
			"hex":          true,
			"gbk_decode":   true,
			"video_sniff":  true,
			"text_cap_mib": textCap >> 20,
		},
	}
	b.WebView2 = probeWebView2()
	b.Players = probePlayers()
	return b
}

func probeWebView2() WebView2Info {
	info := WebView2Info{
		Media: map[string]bool{
			"h264": true, "vp9": true, "av1": true, "aac": true, "mp3": true,
			"opus": true, "flac": true, "vorbis": true,
			"hevc": false, "mkv": false, "avi": false, "ts": false, "rmvb": false,
			"tiff": false, "heic": false, "psd": false,
		},
	}

	roots := []struct {
		k    registry.Key
		path string
		name string
	}{
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\` + wv2ClientGUID, "HKLM/WOW6432Node"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + wv2ClientGUID, "HKLM"},
		{registry.CURRENT_USER, `SOFTWARE\Microsoft\EdgeUpdate\Clients\` + wv2ClientGUID, "HKCU"},
	}
	for _, r := range roots {
		k, err := registry.OpenKey(r.k, r.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		pv, _, err := k.GetStringValue("pv")
		k.Close()
		if err != nil || pv == "" {
			continue
		}
		info.Version = pv
		info.Source = r.name
		break
	}

	// 版本号拿到还不够，得确认 exe 真在盘上
	for _, dir := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "EdgeWebView", "Application"),
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "EdgeWebView", "Application"),
	} {
		if dir == "" {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "msedgewebview2.exe"))
		if len(matches) == 0 {
			continue
		}
		info.Present = true
		info.RuntimeDir = filepath.Dir(matches[len(matches)-1])
		if info.Version == "" {
			info.Version = filepath.Base(info.RuntimeDir)
			info.Source = "目录扫描"
		}
		break
	}

	if !info.Present {
		info.Notes = append(info.Notes,
			"未找到 WebView2 Runtime：Windows 7 / LTSC 精简版常缺，需装 Evergreen Runtime（Win7 最高 109.0.1518.140，已停安全更新）")
	}
	if info.Present {
		info.Notes = append(info.Notes,
			"HEVC 取决于系统 HEVC Video Extensions，本探测按未安装处理；H.264/VP9/AV1 走 Chromium 内置解码器")
	}
	return info
}

// 候选顺序 = 降级优先级：mpv 对图片/稀有格式最宽容，排第一。
var playerCandidates = []Player{
	{Name: "mpv", Path: "mpv.exe", Video: true, Audio: true, Image: true},
	{Name: "VLC", Path: "vlc.exe", Video: true, Audio: true, Image: true},
	{Name: "PotPlayer", Path: "PotPlayerMini64.exe", Video: true, Audio: true, Image: true},
	{Name: "MPC-BE", Path: "mpc-be64.exe", Video: true, Audio: true, Image: false},
	{Name: "MPC-HC", Path: "mpc-hc64.exe", Video: true, Audio: true, Image: false},
	{Name: "ffplay", Path: "ffplay.exe", Video: true, Audio: true, Image: true},
	{Name: "vlc(x86)", Path: "vlc.exe", Video: true, Audio: true, Image: true},
}

var playerDirs = []string{
	`C:\Program Files\mpv`,
	`C:\Program Files (x86)\mpv`,
	`C:\Program Files\mpv.net`,
	`C:\Tools\mpv`,
	`C:\Program Files\VideoLAN\VLC`,
	`C:\Program Files (x86)\VideoLAN\VLC`,
	`C:\Program Files\DAUM\PotPlayer`,
	`C:\Program Files (x86)\DAUM\PotPlayer`,
	`C:\Program Files\MPC-BE x64`,
	`C:\Program Files\MPC-HC`,
	`C:\Program Files\MPC-HC\MPC-HC64`,
	`C:\Program Files\ffmpeg\bin`,
}

func probePlayers() []Player {
	seen := map[string]bool{}
	var out []Player

	add := func(p Player, path string) {
		key := strings.ToLower(path)
		if seen[key] {
			return
		}
		seen[key] = true
		p.Path = path
		out = append(out, p)
	}

	for _, c := range playerCandidates {
		if lp, err := exec.LookPath(c.Path); err == nil {
			add(c, lp)
			continue
		}
		for _, d := range playerDirs {
			cand := filepath.Join(d, c.Path)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				add(c, cand)
				break
			}
		}
	}
	return out
}

// PickPlayer 挑一个能干这活儿的后端。needVideo 时图片也算（mpv/VLC 都能看图）。
func (b *Backends) PickPlayer(video, audio bool) *Player {
	if b == nil {
		return nil
	}
	for i := range b.Players {
		p := &b.Players[i]
		if video && (p.Video || p.Image) {
			return p
		}
		if audio && p.Audio {
			return p
		}
	}
	return nil
}

// ProbeReport 是 --probe --json 的完整信封。
type ProbeReport struct {
	OK            bool      `json:"ok"`
	SchemaVersion int       `json:"schema_version"`
	Target        *Plan     `json:"target,omitempty"`
	Backends      *Backends `json:"backends"`
	Degraded      string    `json:"degraded_reason,omitempty"`
	Error         string    `json:"error,omitempty"`
}

func (r *ProbeReport) JSON() string {
	b, _ := json.MarshalIndent(r, "", "  ")
	return string(b)
}
