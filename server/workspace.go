package server

import (
	"bytes"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Workspace file manager — browse / view / edit / download / upload / delete the
// shared work dir (s.m.dir), where all agents write their artifacts. Every path is
// confined to the work dir root (traversal via ".." is neutralised). All routes sit
// behind requireAuth (see Handler()).

const (
	maxWorkspaceRead   = 2 << 20   // 2 MiB: files bigger than this aren't inlined for view/edit (download instead)
	maxWorkspaceUpload = 512 << 20 // 512 MiB per upload request
)

// wsResolve preserves the original rooted path normalization. It returns a
// relative name only; every filesystem operation must use the opened os.Root.
func (s *Server) wsResolve(rel string) (string, bool) {
	rel = strings.TrimPrefix(strings.TrimSpace(rel), "/")
	clean := filepath.Clean("/" + rel)
	name := strings.TrimPrefix(clean, string(os.PathSeparator))
	if name == "" {
		name = "."
	}
	return name, filepath.IsLocal(name)
}

func (s *Server) wsRel(name string) string {
	if name == "." {
		return ""
	}
	return filepath.ToSlash(name)
}

// Root operations enforce containment during resolution, including symlink
// swaps. Valid relative symlinks inside the workspace continue to work.
func (s *Server) wsRoot(w http.ResponseWriter) *os.Root {
	root, err := os.OpenRoot(s.m.dir)
	if err != nil {
		writeErr(w, 500, "工作区不可用")
		return nil
	}
	return root
}

type wsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"` // unix millis
}

// GET /api/workspace/list?path=<rel>
func (s *Server) wsList(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	rel, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	fi, err := root.Stat(rel)
	if err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	if !fi.IsDir() {
		writeErr(w, 400, "不是目录")
		return
	}
	ents, err := fs.ReadDir(root.FS(), filepath.ToSlash(rel))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]wsEntry, 0, len(ents))
	for _, e := range ents {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, wsEntry{
			Name:  e.Name(),
			Path:  s.wsRel(filepath.Join(rel, e.Name())),
			Dir:   e.IsDir(),
			Size:  info.Size(),
			MTime: info.ModTime().UnixMilli(),
		})
	}
	// 目录在前，各自按名称排序。
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	writeJSON(w, 200, map[string]any{"path": s.wsRel(rel), "entries": out})
}

// GET /api/workspace/read?path=<rel> — inline text for view/edit. Binary or oversize
// files return {binary:true}/{too_large:true} with no content (use download instead).
func (s *Server) wsRead(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	rel, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, 400, "不是普通文件")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxWorkspaceRead+1))
	if err != nil {
		writeErr(w, 500, "读取失败")
		return
	}
	if len(data) > maxWorkspaceRead {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(rel), "size": fi.Size(), "too_large": true, "binary": true})
		return
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		writeJSON(w, 200, map[string]any{"path": s.wsRel(rel), "size": fi.Size(), "binary": true})
		return
	}
	writeJSON(w, 200, map[string]any{"path": s.wsRel(rel), "size": fi.Size(), "binary": false, "content": string(data)})
}

// POST /api/workspace/write  {path, content} — create/overwrite a text file.
func (s *Server) wsWrite(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rel, ok := s.wsResolve(req.Path)
	if !ok || rel == "." {
		writeErr(w, 400, "非法路径")
		return
	}
	if fi, err := root.Stat(rel); err == nil && fi.IsDir() {
		writeErr(w, 400, "目标是目录")
		return
	}
	if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := root.WriteFile(rel, []byte(req.Content), 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(rel)})
}

// POST /api/workspace/mkdir  {path}
func (s *Server) wsMkdir(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rel, ok := s.wsResolve(req.Path)
	if !ok || rel == "." {
		writeErr(w, 400, "非法路径")
		return
	}
	if err := root.MkdirAll(rel, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": s.wsRel(rel)})
}

// DELETE /api/workspace/delete?path=<rel> — removes a file or a directory tree
// (confined to the work dir; the root itself can't be deleted).
func (s *Server) wsDelete(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	rel, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	if rel == "." {
		writeErr(w, 400, "不能删除工作区根目录")
		return
	}
	if _, err := root.Lstat(rel); err != nil {
		writeErr(w, 404, "路径不存在")
		return
	}
	if err := root.RemoveAll(rel); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// GET /api/workspace/download?path=<rel> — stream a file as an attachment.
func (s *Server) wsDownload(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	rel, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	f, err := root.Open(rel)
	if err != nil {
		writeErr(w, 404, "文件不存在")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, 404, "文件不存在")
		return
	}
	name := filepath.Base(rel)
	// RFC 5987 filename* keeps non-ASCII names intact; plain filename is the fallback.
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"; filename*=UTF-8''"+url.PathEscape(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// POST /api/workspace/upload?path=<dir> — multipart form field "file" (one or more).
func (s *Server) wsUpload(w http.ResponseWriter, r *http.Request) {
	root := s.wsRoot(w)
	if root == nil {
		return
	}
	defer root.Close()
	dirRel, ok := s.wsResolve(r.URL.Query().Get("path"))
	if !ok {
		writeErr(w, 400, "非法路径")
		return
	}
	if fi, err := root.Stat(dirRel); err != nil || !fi.IsDir() {
		writeErr(w, 400, "目标目录不存在")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUpload)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "解析上传失败或超出大小限制："+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeErr(w, 400, "缺少上传文件(表单字段 file)")
		return
	}
	saved := 0
	for _, hdr := range files {
		name := filepath.Base(hdr.Filename) // strip any path component
		if name == "" || name == "." || name == ".." {
			continue
		}
		destRel, okd := s.wsResolve(filepath.Join(s.wsRel(dirRel), name))
		if !okd {
			continue
		}
		if err := saveUpload(root, hdr, destRel, false); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		saved++
	}
	writeJSON(w, 200, map[string]any{"uploaded": saved})
}

func saveUpload(root *os.Root, hdr *multipart.FileHeader, dest string, exclusive bool) error {
	src, err := hdr.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if exclusive {
		flags = os.O_WRONLY | os.O_CREATE | os.O_EXCL
	}
	out, err := root.OpenFile(dest, flags, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename token.
func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\\", "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	return name
}
