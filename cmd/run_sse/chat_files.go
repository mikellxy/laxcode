package run_sse

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mikellxy/laxcode/internal/domain/session"
	"github.com/mikellxy/laxcode/internal/infrastructure/sessionrepo"
)

const maxChatDownloadBytes int64 = 100 << 20
const maxChatDownloadFiles = 200

func (s *server) openChatFileProject(w http.ResponseWriter, r *http.Request) (*os.Root, string, []session.ChatFile) {
	if s.userRoot == "" {
		writeJSONError(w, http.StatusForbidden, "file downloads are only available in cloud mode")
		return nil, "", nil
	}
	repo, ok := s.contextRepo.(session.ChatFilesRepository)
	if !ok || s.catalog == nil {
		writeJSONError(w, 500, "file repository is unavailable")
		return nil, "", nil
	}
	sessionID, chatID := r.PathValue("session_id"), r.PathValue("chat_id")
	selected, err := s.catalog.GetSession(r.Context(), sessionID)
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
	root, err := os.OpenRoot(s.userRoot)
	if err != nil {
		writeJSONError(w, 500, "用户目录不可用")
		return nil, "", nil
	}
	defer root.Close()
	projectPath, err := filepath.Rel(s.userRoot, selected.WorkDir)
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

func (s *server) handleDownloadChatFiles(w http.ResponseWriter, r *http.Request) {
	project, projectPath, files := s.openChatFileProject(w, r)
	if project == nil {
		return
	}
	defer project.Close()
	if len(files) > maxChatDownloadFiles {
		writeJSONError(w, 413, "下载文件数量超过 200 个")
		return
	}
	root, err := os.OpenRoot(s.userRoot)
	if err != nil {
		writeJSONError(w, 500, "用户目录不可用")
		return
	}
	defer root.Close()
	tempPath, err := filepath.Rel(s.userRoot, s.tempDir)
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
	w.Header().Set("Content-Disposition", `attachment; filename="chat-files.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "chat-files.zip", time.Time{}, archive)
}
