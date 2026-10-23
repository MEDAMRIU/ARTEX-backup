package server

import (
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Autumn-27/artex/report"
)

// collectReportArtifacts indexes only this task's workspace. os.Root confines all
// reads; symlinks and special files are excluded, including symlinked directories.
func collectReportArtifacts(workspace, taskID string) ([]report.Artifact, []report.Assessment, []string) {
	var artifacts []report.Artifact
	var assessments []report.Assessment
	var limits []string
	id, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != taskID {
		return nil, nil, []string{"Invalid task artifact root."}
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, nil, []string{"Workspace unavailable; artifact export is incomplete."}
	}
	defer root.Close()
	base := path.Join("tasks", taskID)
	var expectedTask os.FileInfo
	for _, dir := range []string{"tasks", base} {
		info, err := root.Lstat(dir)
		if os.IsNotExist(err) {
			return nil, nil, []string{"No task artifact directory exists."}
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, []string{"Task artifact root is not a regular directory."}
		}
		if dir == base {
			expectedTask = info
		}
	}
	taskRoot, err := root.OpenRoot(base)
	if err != nil {
		return nil, nil, []string{"Cannot open task artifact directory."}
	}
	defer taskRoot.Close()
	openedTask, err := taskRoot.Stat(".")
	if err != nil || !os.SameFile(expectedTask, openedTask) {
		return nil, nil, []string{"Task artifact root changed during export; retry after work stops."}
	}
	remaining := int64(2 << 20)
	entries := 0
	err = fs.WalkDir(taskRoot.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			limits = append(limits, fmt.Sprintf("Cannot read artifact %q.", name))
			return nil
		}
		entries++
		if entries > 4000 {
			limits = append(limits, "Artifact listing stopped after 4,000 directory entries.")
			return fs.SkipAll
		}
		if entry.IsDir() {
			// Browser trust stores and cache are runtime state, not assessment
			// evidence; do not bury the useful artifacts under generated files.
			if name == ".runtime-home" {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			limits = append(limits, fmt.Sprintf("Excluded non-regular or unreadable artifact %q.", name))
			return nil
		}
		canonical := path.Join(base, name)
		artifacts = append(artifacts, report.Artifact{Path: canonical, Size: info.Size(), URL: "/api/workspace/download?path=" + url.QueryEscape(canonical)})
		fileName := strings.ToLower(path.Base(name))
		if fileName != "report.md" && fileName != "assessment.md" && fileName != "final-report.md" {
			return nil
		}
		if info.Size() > 512<<10 || info.Size() > remaining {
			limits = append(limits, fmt.Sprintf("Assessment %q is indexed but exceeds the inline size limit.", canonical))
			return nil
		}
		f, err := openReportFile(taskRoot, name)
		if err != nil {
			limits = append(limits, fmt.Sprintf("Cannot read assessment %q.", canonical))
			return nil
		}
		opened, err := f.Stat()
		if err != nil || !opened.Mode().IsRegular() {
			_ = f.Close()
			limits = append(limits, fmt.Sprintf("Assessment %q changed type during export.", canonical))
			return nil
		}
		content, readErr := io.ReadAll(io.LimitReader(f, min(remaining, 512<<10)+1))
		_ = f.Close()
		if readErr != nil || int64(len(content)) > min(remaining, 512<<10) || !utf8.Valid(content) {
			limits = append(limits, fmt.Sprintf("Assessment %q could not be inlined completely.", canonical))
			return nil
		}
		remaining -= int64(len(content))
		assessments = append(assessments, report.Assessment{Path: canonical, Content: string(content)})
		return nil
	})
	if err != nil {
		limits = append(limits, "Artifact listing is incomplete: "+err.Error())
	}
	return artifacts, assessments, limits
}
