package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func workspaceFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	base := t.TempDir()
	inside, outside := filepath.Join(base, "workspace"), filepath.Join(base, "outside")
	for _, d := range []string{inside, outside} {
		if err := os.Mkdir(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	return &Server{m: &Manager{dir: inside}}, inside, outside
}

func workspaceUploadRequest(t *testing.T, path, name, content string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	f, err := m.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	m.Close()
	r := httptest.NewRequest("POST", path, &body)
	r.Header.Set("Content-Type", m.FormDataContentType())
	return r
}

func TestWorkspaceRejectsEscapingSymlinks(t *testing.T) {
	s, inside, outside := workspaceFixture(t)
	if err := os.Symlink(outside, filepath.Join(inside, "escape")); err != nil {
		t.Skip(err)
	}
	cases := []struct {
		name              string
		handler           http.HandlerFunc
		method, url, body string
	}{
		{"read", s.wsRead, "GET", "/api/workspace/read?path=escape/secret", ""},
		{"download", s.wsDownload, "GET", "/api/workspace/download?path=escape/secret", ""},
		{"list", s.wsList, "GET", "/api/workspace/list?path=escape", ""},
		{"write", s.wsWrite, "POST", "/api/workspace/write", `{"path":"escape/secret","content":"overwritten"}`},
		{"mkdir", s.wsMkdir, "POST", "/api/workspace/mkdir", `{"path":"escape/new"}`},
		{"delete", s.wsDelete, "DELETE", "/api/workspace/delete?path=escape/secret", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c.handler(w, httptest.NewRequest(c.method, c.url, strings.NewReader(c.body)))
			if w.Code < 400 {
				t.Fatalf("escape accepted: %d %s", w.Code, w.Body)
			}
		})
	}
	w := httptest.NewRecorder()
	s.wsUpload(w, workspaceUploadRequest(t, "/api/workspace/upload?path=escape", "secret", "overwritten"))
	if w.Code < 400 {
		t.Fatal("upload escaped")
	}
	// A link in the upload destination filename must also be rejected.
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(inside, "secret")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.wsUpload(w, workspaceUploadRequest(t, "/api/workspace/upload", "secret", "overwritten"))
	if w.Code < 400 {
		t.Fatal("upload followed final symlink")
	}
	got, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(got) != "outside-secret" {
		t.Fatalf("outside changed: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("mkdir escaped")
	}
}

func TestWorkspaceNormalOperationsAndInternalSymlinks(t *testing.T) {
	s, inside, _ := workspaceFixture(t)
	request := func(h http.HandlerFunc, method, url, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(method, url, strings.NewReader(body)))
		if w.Code >= 400 {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body)
		}
		return w
	}
	request(s.wsMkdir, "POST", "/api/workspace/mkdir", `{"path":"nested"}`)
	request(s.wsWrite, "POST", "/api/workspace/write", `{"path":"nested/file.txt","content":"hello"}`)
	if err := os.Symlink("nested", filepath.Join(inside, "alias")); err != nil {
		t.Skip(err)
	}
	w := request(s.wsRead, "GET", "/api/workspace/read?path=alias/file.txt", "")
	var data struct{ Content string }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || data.Content != "hello" {
		t.Fatalf("read: %s %v", w.Body, err)
	}
	w = request(s.wsDownload, "GET", "/api/workspace/download?path=alias/file.txt", "")
	if w.Body.String() != "hello" {
		t.Fatal(w.Body)
	}
	request(s.wsList, "GET", "/api/workspace/list", "")
	w = httptest.NewRecorder()
	s.wsUpload(w, workspaceUploadRequest(t, "/api/workspace/upload?path=nested", "upload.txt", "upload"))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	request(s.wsDelete, "DELETE", "/api/workspace/delete?path=nested", "")
	if _, err := os.Stat(filepath.Join(inside, "nested")); !os.IsNotExist(err) {
		t.Fatal("directory not removed")
	}
	w = httptest.NewRecorder()
	s.wsDelete(w, httptest.NewRequest("DELETE", "/api/workspace/delete?path=..", nil))
	if w.Code != 400 {
		t.Fatal("root deletion accepted")
	}
}

func TestWorkspaceSymlinkSwapCannotReadOutside(t *testing.T) {
	s, inside, outside := workspaceFixture(t)
	if err := os.Mkdir(filepath.Join(inside, "safe"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "safe", "secret"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(inside, "switch")
	if err := os.Symlink("safe", link); err != nil {
		t.Skip(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, target := range []string{outside, "safe"} {
				os.Remove(link)
				os.Symlink(target, link)
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for i := 0; i < 100; i++ {
		w := httptest.NewRecorder()
		s.wsRead(w, httptest.NewRequest("GET", "/api/workspace/read?path=switch/secret", nil))
		if strings.Contains(w.Body.String(), "outside-secret") {
			t.Fatal("read escaped during swap")
		}
	}
}

func TestChatUploadConfinedAndDoesNotOverwrite(t *testing.T) {
	s, inside, outside := workspaceFixture(t)
	if err := os.MkdirAll(filepath.Join(inside, "drafts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inside, "drafts", "escape")); err != nil {
		t.Skip(err)
	}
	w := httptest.NewRecorder()
	s.chatUpload(w, workspaceUploadRequest(t, "/api/chat/upload?scope=staging&id=escape", "secret", "bad"))
	if w.Code < 400 {
		t.Fatal("chat upload escaped")
	}
	for _, content := range []string{"first", "second"} {
		w = httptest.NewRecorder()
		s.chatUpload(w, workspaceUploadRequest(t, "/api/chat/upload?scope=staging&id=normal", "file.txt", content))
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	}
	for name, want := range map[string]string{"file.txt": "first", "file-1.txt": "second"} {
		got, err := os.ReadFile(filepath.Join(inside, "drafts", "normal", "uploads", name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

func TestSkillEditorRejectsEscapingSymlinks(t *testing.T) {
	s, inside, outside := workspaceFixture(t)
	s.skillDir = inside
	if err := os.Mkdir(filepath.Join(inside, "sample"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inside, "sample", "escape")); err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		method     string
		handler    http.HandlerFunc
		file, body string
	}{
		{"GET", s.fsReadFile, "escape/secret", ""},
		{"POST", s.fsWriteFile, "escape/secret", `{"content":"bad"}`},
		{"POST", s.fsCreateDir, "", `{"path":"escape/new"}`},
		{"DELETE", s.fsDeletePath, "escape/secret", ""},
	} {
		r := httptest.NewRequest(tc.method, "/", strings.NewReader(tc.body))
		r.SetPathValue("name", "sample")
		r.SetPathValue("file", tc.file)
		w := httptest.NewRecorder()
		tc.handler(w, r)
		if w.Code < 400 {
			t.Fatalf("skill editor escaped: %s", tc.method)
		}
	}
	if err := os.Symlink(outside, filepath.Join(inside, "linked")); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.SetPathValue("name", "linked")
	r.SetPathValue("file", "secret")
	w := httptest.NewRecorder()
	s.fsReadFile(w, r)
	if w.Code < 400 {
		t.Fatal("symlinked skill root escaped")
	}
	got, err := os.ReadFile(filepath.Join(outside, "secret"))
	if err != nil || string(got) != "outside-secret" {
		t.Fatal("outside skill file changed")
	}
}
