package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxReadLines   = 2000
	maxReadBytes   = 100 * 1024 // stop adding lines once the output reaches this size
	maxLineLen     = 2000
	maxListEntries = 1000
)

// skipDirs are not descended into by list_files unless listed directly.
var skipDirs = map[string]bool{
	".git":   true,
	"ignore": true, // gitignored scratch directory in this repo
}

// workDir is the project root, set at startup. File tools only touch paths
// inside it.
var workDir string

// resolvePath turns a path from the model into an absolute path and rejects
// anything outside workDir. It does not follow symlinks, so a symlink inside
// the project that points outside it would still get through.
func resolvePath(p string) (string, error) {
	if p == "" {
		p = "."
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(workDir, abs)
	}
	abs = filepath.Clean(abs)

	rel, err := filepath.Rel(workDir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the project", p)
	}
	return abs, nil
}

func readFile(input json.RawMessage) (string, error) {
	var args struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("path is required")
	}

	path, err := resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
		return "", fmt.Errorf("%s looks like a binary file", args.Path)
	}
	if len(data) == 0 {
		return "(empty file)", nil
	}

	lines := strings.Split(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // trailing newline, not extra line
	}

	offset := max(args.Offset, 1)
	limit := args.Limit
	if limit <= 0 || limit > maxReadLines {
		limit = maxReadLines
	}
	if offset > len(lines) {
		return "", fmt.Errorf("offset %d is past the end of the file (%d lines)", offset, len(lines))
	}

	var out strings.Builder
	end := min(offset-1+limit, len(lines))
	for i := offset - 1; i < end; i++ {
		text := lines[i]
		if len(text) > maxLineLen {
			text = text[:maxLineLen] + " ... (line truncated)"
		}
		line := fmt.Sprintf("%6d\t%s\n", i+1, text)
		if out.Len()+len(line) > maxReadBytes {
			end = i
			break
		}
		out.WriteString(line)
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "(showing lines %d-%d of %d; use offset to read more)\n", offset, end, len(lines))
	}
	return out.String(), nil
}

func listFiles(input json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}

	root, err := resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", args.Path)
	}

	var files []string
	truncated := false
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil // skip anything unreadable
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxListEntries {
			truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(workDir, p)
		if err != nil {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(files) == 0 {
		return "(no files)", nil
	}
	out := strings.Join(files, "\n")
	if truncated {
		out += fmt.Sprintf("\n(stopped after %d files; list a subdirectory to see more)", maxListEntries)
	}
	return out, nil
}
