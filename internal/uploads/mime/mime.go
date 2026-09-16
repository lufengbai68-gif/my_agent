// Package mime 用 magic bytes 检测 MIME 类型和图片尺寸。
//
// 不用扩展名: 用户可能把 .exe 改成 .png 绕过白名单。
// 只识别白名单里那几种, 不做通用 detection, 节省复杂度。
package mime

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var (
	pngHeader = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	jpegSOI   = []byte{0xFF, 0xD8}
	webpRIFF  = []byte("RIFF")
	gifGIF    = []byte("GIF8")
)

// Detect 从 reader 读 magic bytes, 返回 MIME。
//
// 注意: reader 会被被读取, 调用方需要保证 reader 可重读或者先 peek 再 reset。
// 本项目用 bufio + reset 模式, 见 server 端调用。
func Detect(r io.Reader) string {
	// 先读 32 字节做 magic 检测
	var buf [32]byte
	n, _ := io.ReadFull(r, buf[:])
	if n < 8 {
		return ""
	}
	head := buf[:n]

	switch {
	case bytes.HasPrefix(head, pngHeader):
		return "image/png"
	case bytes.HasPrefix(head, jpegSOI):
		return "image/jpeg"
	case bytes.HasPrefix(head, gifGIF):
		return "image/gif"
	case bytes.HasPrefix(head, webpRIFF) && n >= 12 && string(head[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// DetectByFilename 用扩展名猜（兜底, 没 magic 时用）。
func DetectByFilename(name string) string {
	switch strings.ToLower(extOf(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

// ExtFromMime 从 MIME 推后缀。
func ExtFromMime(m string) string {
	switch strings.ToLower(m) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ""
}

// Dimensions 读 reader 头部, 返回 (width, height)。失败返回 (0,0)。
//
// 支持 PNG / JPEG / WebP。GIF 不解析尺寸（用得少）。
//
// 注意: reader 被读走 magic + 头部, 调用方需要保证 reader 可重读或提前 seek。
func Dimensions(r io.Reader, mimeType string) (int, int) {
	if r == nil {
		return 0, 0
	}
	// 读 64KB 应该够
	var buf [64 * 1024]byte
	n, _ := io.ReadFull(r, buf[:])
	if n == 0 {
		return 0, 0
	}
	data := buf[:n]

	switch strings.ToLower(mimeType) {
	case "image/png":
		return pngDims(data)
	case "image/jpeg":
		return jpegDims(data)
	case "image/webp":
		return webpDims(data)
	}
	return 0, 0
}

func pngDims(b []byte) (int, int) {
	if len(b) < 24 {
		return 0, 0
	}
	// IHDR 在第 8-24 字节: width 4 bytes (offset 16), height 4 bytes (offset 20)
	if !bytes.HasPrefix(b, pngHeader) {
		return 0, 0
	}
	w := binary.BigEndian.Uint32(b[16:20])
	h := binary.BigEndian.Uint32(b[20:24])
	return int(w), int(h)
}

// jpegDims 解析 JPEG 的 SOF 段拿宽高。
//
// 简化: 不查 marker 链, 只在 0xFFC0 / 0xFFC1 / 0xFFC2 处取尺寸。
func jpegDims(b []byte) (int, int) {
	if len(b) < 4 || !bytes.HasPrefix(b, jpegSOI) {
		return 0, 0
	}
	i := 2
	for i+9 < len(b) {
		if b[i] != 0xFF {
			i++
			continue
		}
		// 跳过 0xFF 填充
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			return 0, 0
		}
		marker := b[i]
		i++
		// SOF0/SOF1/SOF2/SOF3/SOF5/SOF6/SOF7/SOF9/SOF10/SOF11/SOF13/SOF14/SOF15
		if marker >= 0xC0 && marker <= 0xC3 ||
			marker >= 0xC5 && marker <= 0xC7 ||
			marker >= 0xC9 && marker <= 0xCB ||
			marker >= 0xCD && marker <= 0xCF {
			if i+7 > len(b) {
				return 0, 0
			}
			// b[i+0:i+2] = length, b[i+2] = precision
			// b[i+3:i+5] = height, b[i+5:i+7] = width
			h := binary.BigEndian.Uint16(b[i+3 : i+5])
			w := binary.BigEndian.Uint16(b[i+5 : i+7])
			return int(w), int(h)
		}
		// 其他 marker: 跳 length
		if i+2 > len(b) {
			return 0, 0
		}
		segLen := int(binary.BigEndian.Uint16(b[i : i+2]))
		i += segLen
	}
	return 0, 0
}

// webpDims 解析 WebP 头: RIFF + 4字节size + WEBP + (VP8/VP8L/VP8X/...) + 尺寸
func webpDims(b []byte) (int, int) {
	if len(b) < 30 || !bytes.HasPrefix(b, webpRIFF) {
		return 0, 0
	}
	if string(b[8:12]) != "WEBP" {
		return 0, 0
	}
	chunk := string(b[12:16])
	off := 16
	switch chunk {
	case "VP8 ":
		// VP8 简单格式: 0x9D 0x01 0x2A 头 + width/height
		if len(b) < off+23 {
			return 0, 0
		}
		w := binary.LittleEndian.Uint16(b[off+6 : off+8])
		h := binary.LittleEndian.Uint16(b[off+8 : off+10])
		return int(w) & 0x3FFF, int(h) & 0x3FFF
	case "VP8L":
		// VP8L: b 0x2F 头 + 14-bit width-1, 14-bit height-1
		if len(b) < off+8 {
			return 0, 0
		}
		// 第一个 uint32: signature (0x2F) | 14-bit width-1 | 14-bit height-1
		// 但 signature 是 0x2F 占低 8 位, layout 见 VP8L spec
		// b[0] must be 0x2F
		if b[off] != 0x2F {
			return 0, 0
		}
		u := binary.LittleEndian.Uint32(b[off+1 : off+5])
		w := (u & 0x3FFF) + 1
		h := ((u >> 14) & 0x3FFF) + 1
		return int(w), int(h)
	case "VP8X":
		if len(b) < off+10 {
			return 0, 0
		}
		w := binary.LittleEndian.Uint16(b[off+4 : off+6]) & 0x3FFF
		h := binary.LittleEndian.Uint16(b[off+6 : off+8]) & 0x3FFF
		return int(w) + 1, int(h) + 1
	}
	return 0, 0
}

func extOf(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

var ErrUnsupported = errors.New("unsupported image format")
