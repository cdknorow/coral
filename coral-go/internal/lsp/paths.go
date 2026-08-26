package lsp

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func Canonical(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func ResolveFile(boundary, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("invalid filepath")
	}
	root, err := Canonical(boundary)
	if err != nil {
		return "", err
	}
	candidate, err := Canonical(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	if !Within(root, candidate) {
		return "", fmt.Errorf("path outside workspace")
	}
	if candidate == root {
		return "", fmt.Errorf("workspace root is not a document")
	}
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return "", fmt.Errorf("document path is a directory")
	}
	return candidate, nil
}

// ResolveWorkspace finds the closest marker, with earlier markers taking precedence.
func ResolveWorkspace(boundary, file string, markers []string) (string, error) {
	root, err := Canonical(boundary)
	if err != nil {
		return "", err
	}
	path, err := Canonical(file)
	if err != nil {
		return "", err
	}
	if !Within(root, path) {
		return "", fmt.Errorf("file outside worktree")
	}
	for _, marker := range markers {
		for dir := filepath.Dir(path); Within(root, dir); dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
			if dir == root {
				break
			}
		}
	}
	return root, nil
}

func PathToURI(path string) (string, error) {
	p, err := Canonical(path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		p = "/" + filepath.ToSlash(p)
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(p)}).String(), nil
}

func URIToPath(workspace, uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return "", fmt.Errorf("invalid file URI")
	}
	p := filepath.FromSlash(u.Path)
	if runtime.GOOS == "windows" && len(p) > 2 && p[0] == filepath.Separator && p[2] == ':' {
		p = p[1:]
	}
	p, err = Canonical(p)
	if err != nil {
		return "", err
	}
	root, err := Canonical(workspace)
	if err != nil {
		return "", err
	}
	if !Within(root, p) {
		return "", fmt.Errorf("location outside workspace")
	}
	return p, nil
}
