package workspace

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

const maxChatDownloadBytes int64 = 100 << 20
const maxChatDownloadFiles = 200

func (s *API) openChatFileProject(w http.ResponseWriter, r *http.Request) (*os.Root, string, []session.ChatFile) {

	repo, ok := s.ContextRepo.(session.ChatFilesRepository)
	if !ok || s.Catalog == nil {
		writeJSONError(w, 500, "file repository is unavailable")
		return nil, "", nil
	}
	sessionID, chatID := r.PathValue("session_id"), r.PathValue("chat_id")
	selected, err := s.Catalog.GetSession(r.Context(), sessionID)
	if err != nil {
		writeJSONError(w, 404, "session not found")
		return nil, "", nil
	}
	files, err := repo.ListChatFiles(r.Context(), sessionID, chatID)
	if errors.Is(err, sessionrepo.ErrChatNotFound) || (err == nil && len(files) == 0) {
		writeJSONError(w, 404, "chat has no downloadable files")
		return nil, "", nil
	}
	if err != nil {
		writeJSONError(w, 500, "无法读取文件清单")
		return nil, "", nil
	}
	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		writeJSONError(w, 500, "用户目录不可用")
		return nil, "", nil
	}
	defer root.Close()
	projectPath, err := s.projectPath(selected.WorkDir)
	if err != nil || !fs.ValidPath(filepath.ToSlash(projectPath)) || projectPath == "." {
		writeJSONError(w, 403, "项目目录越界")
		return nil, "", nil
	}
	project, err := root.OpenRoot(projectPath)
	if err != nil {
		writeJSONError(w, 403, "项目目录不可用")
		return nil, "", nil
	}
	return project, projectPath, files
}

func openChatFile(w http.ResponseWriter, project *os.Root, projectPath, path string) (*os.File, fs.FileInfo) {
	if !fs.ValidPath(path) || strings.Contains(path, "\\") {
		writeJSONError(w, 403, "文件路径越界")
		return nil, nil
	}
	rel, err := filepath.Rel(projectPath, filepath.FromSlash(path))
	if err != nil || !fs.ValidPath(filepath.ToSlash(rel)) || rel == "." {
		writeJSONError(w, 403, "文件路径越界")
		return nil, nil
	}
	info, err := project.Lstat(rel)
	if err != nil || !info.Mode().IsRegular() {
		writeJSONError(w, 409, "文件已删除或不是普通文件：/"+path)
		return nil, nil
	}
	// Nonblocking open prevents a concurrent swap to a FIFO from hanging.
	input, err := project.OpenFile(rel, downloadOpenFlags, 0)
	if err != nil {
		writeJSONError(w, 409, "文件不可读取：/"+path)
		return nil, nil
	}
	info, err = input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = input.Close()
		writeJSONError(w, 409, "文件不是普通文件：/"+path)
		return nil, nil
	}
	return input, info
}

func (s *API) HandleDownloadChatFiles(w http.ResponseWriter, r *http.Request) {
	project, projectPath, files := s.openChatFileProject(w, r)
	if project == nil {
		return
	}
	defer project.Close()
	selected, err := s.Catalog.GetSession(r.Context(), r.PathValue("session_id"))
	if err != nil {
		writeJSONError(w, 404, "session not found")
		return
	}
	downloadName := chatZIPFilename(selected.Title, time.Now())
	if len(files) > maxChatDownloadFiles {
		writeJSONError(w, 413, "下载文件数量超过 200 个")
		return
	}
	root, err := os.OpenRoot(s.UserRoot)
	if err != nil {
		writeJSONError(w, 500, "用户目录不可用")
		return
	}
	defer root.Close()
	tempPath, err := filepath.Rel(s.UserRoot, s.TempDir)
	if err != nil || !fs.ValidPath(filepath.ToSlash(tempPath)) || tempPath == "." {
		writeJSONError(w, 500, "临时目录不可用")
		return
	}
	temp, err := root.OpenRoot(tempPath)
	if err != nil {
		writeJSONError(w, 500, "临时目录不可用")
		return
	}
	defer temp.Close()
	name := "chat-files-" + uuid.NewString() + ".zip"
	archive, err := temp.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		writeJSONError(w, 500, "无法生成下载文件")
		return
	}
	defer temp.Remove(name)
	defer archive.Close()
	writer := zip.NewWriter(archive)
	defer writer.Close()
	var total int64
	for _, file := range files {
		if r.Context().Err() != nil {
			return
		}
		input, info := openChatFile(w, project, projectPath, file.Path)
		if input == nil {
			return
		}
		if info.Size() > maxChatDownloadBytes-total {
			_ = input.Close()
			writeJSONError(w, 413, "下载文件总大小超过 100 MiB")
			return
		}
		entry, err := writer.Create(file.Path)
		if err != nil {
			_ = input.Close()
			writeJSONError(w, 500, "无法生成下载文件")
			return
		}
		written, copyErr := io.CopyN(entry, input, maxChatDownloadBytes-total+1)
		_ = input.Close()
		total += written
		if total > maxChatDownloadBytes {
			writeJSONError(w, 413, "下载文件总大小超过 100 MiB")
			return
		}
		if copyErr != nil && !errors.Is(copyErr, io.EOF) {
			writeJSONError(w, 409, "读取文件失败：/"+file.Path)
			return
		}
	}
	if err := writer.Close(); err != nil {
		writeJSONError(w, 500, "无法生成下载文件")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": downloadName}))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, downloadName, time.Time{}, archive)
}

func chatZIPFilename(title string, now time.Time) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, title)
	// Keep the complete filename within common filesystem byte limits.
	for len(title) > 180 {
		runes := []rune(title)
		title = string(runes[:len(runes)-1])
	}
	title = strings.Trim(title, " .")
	if title == "" {
		title = "未命名会话"
	}
	return title + "_" + now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("20060102_1504") + ".zip"
}
