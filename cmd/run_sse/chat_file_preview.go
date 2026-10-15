package run_sse

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxChatTextPreviewBytes = 256 << 10
const maxChatImagePreviewBytes = 10 << 20

func (s *server) handlePreviewChatFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	project, projectPath, files := s.openChatFileProject(w, r)
	if project == nil {
		return
	}
	defer project.Close()
	path := r.URL.Query().Get("path")
	listed := false
	for _, file := range files {
		if file.Path == path {
			listed = true
			break
		}
	}
	if !listed {
		writeJSONError(w, 404, "文件不在本次任务的文件清单中")
		return
	}
	input, info := openChatFile(w, project, projectPath, path)
	if input == nil {
		return
	}
	defer input.Close()
	imageType := previewImageType(path)
	limit := int64(maxChatTextPreviewBytes)
	if imageType != "" {
		limit = maxChatImagePreviewBytes
		if info.Size() > limit {
			writeJSONError(w, 413, "图片超过 10 MiB，请下载后查看")
			return
		}
	}
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		writeJSONError(w, 409, "无法读取文件：/"+path)
		return
	}
	if imageType != "" {
		if int64(len(data)) > limit {
			writeJSONError(w, 413, "图片超过 10 MiB，请下载后查看")
			return
		}
		if http.DetectContentType(data) != imageType {
			writeJSONError(w, 415, "文件内容与图片格式不符，请下载后查看")
			return
		}
		w.Header().Set("Content-Type", imageType)
	} else {
		if int64(len(data)) > limit {
			data = data[:limit]
			// A truncated UTF-8 rune can span up to four bytes.
			for i := 0; i < utf8.UTFMax-1 && !utf8.Valid(data); i++ {
				data = data[:len(data)-1]
			}
			w.Header().Set("X-Preview-Truncated", "true")
		}
		if !utf8.Valid(data) || !strings.HasPrefix(http.DetectContentType(data), "text/") {
			writeJSONError(w, 415, "暂不支持预览此文件类型，请下载后查看")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	_, _ = w.Write(data)
}

func previewImageType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}
