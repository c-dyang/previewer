package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// readCapped 读前 n 字节，返回是否被截断。
func readCapped(path string, n int64) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		return buf[:got], false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return buf, st.Size() > n, nil
}

func readHead(path string, n int64) []byte {
	b, _, err := readCapped(path, n)
	if err != nil {
		return nil
	}
	return b
}

func head(b []byte, n int) []byte {
	if len(b) > n {
		return b[:n]
	}
	return b
}

// looksText 是"这能不能当文本读"的判据：UTF-16 BOM 直接认，
// 否则含 NUL 字节一律当二进制。
func looksText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	if bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF}) {
		return true
	}
	sample := b
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	return bytes.IndexByte(sample, 0x00) < 0
}

// decodeBytes 处理编码：BOM 优先 → UTF-8 合法直接收 → 落 GBK（本机 ACP=936）。
func decodeBytes(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], binary.BigEndian)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	if out, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	// 兜底：把非法字节换成 U+FFFD，至少不崩
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

func decodeUTF16(b []byte, order binary.ByteOrder) string {
	n := len(b) / 2
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = order.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}

// hexDump 输出 `xxd` 风格的行：偏移 + 16 字节 + ASCII 侧栏。
func hexDump(b []byte, base int64) string {
	var sb strings.Builder
	sb.Grow(len(b) * 4)
	for off := 0; off < len(b); off += 16 {
		end := off + 16
		if end > len(b) {
			end = len(b)
		}
		row := b[off:end]
		fmt.Fprintf(&sb, "%08x  ", base+int64(off))
		for i := 0; i < 16; i++ {
			if i < len(row) {
				fmt.Fprintf(&sb, "%02x ", row[i])
			} else {
				sb.WriteString("   ")
			}
			if i == 7 {
				sb.WriteByte(' ')
			}
		}
		sb.WriteString(" |")
		for _, c := range row {
			if c >= 0x20 && c < 0x7f {
				sb.WriteByte(c)
			} else {
				sb.WriteByte('.')
			}
		}
		sb.WriteString("|\n")
	}
	return sb.String()
}

// sniffMP4Codec 在头尾各 1 MiB 里找编码四字码。
// 只在 faststart 之外的文件里 moov 会在尾部，所以两头都扫。
func sniffMP4Codec(path string, size int64) string {
	const win = 1 << 20
	cands := []string{"avc1", "avc3", "hvc1", "hev1", "av01", "vp09", "mp4v", "dvhe", "dvh1", "vp08"}

	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, win*2)
	n, _ := io.ReadFull(f, buf[:win])
	data := buf[:n]
	if size > win {
		off := size - win
		if off < win {
			off = win
		}
		if _, err := f.Seek(off, io.SeekStart); err == nil {
			m, _ := io.ReadFull(f, buf[win:])
			data = append(data, buf[win:win+m]...)
		}
	}
	for _, c := range cands {
		if bytes.Contains(data, []byte(c)) {
			return c
		}
	}
	return ""
}

// sniffMKVCodec 只为在报告里写清"为什么解不了"，不参与路由判断。
func sniffMKVCodec(path string, size int64) string {
	data := readHead(path, 1<<20)
	for fourcc, name := range map[string]string{
		"V_MPEG4/ISO/AVC":  "avc1",
		"V_MPEGH/ISO/HEVC": "hvc1",
		"V_AV1":            "av01",
		"V_VP9":            "vp09",
		"V_MPEG4/ISO/ASP":  "mp4v",
	} {
		if bytes.Contains(data, []byte(fourcc)) {
			return name
		}
	}
	return ""
}
