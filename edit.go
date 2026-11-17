package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	previewContext  = 2  // unchanged lines shown before and after a change
	maxPreviewLines = 40 // per side; longer changes are cut in the preview only
)

// confirmFunc asks the user a yes/no question and reports whether they said
// yes. It returns false if ctx is cancelled while waiting for the answer.
type confirmFunc func(ctx context.Context, question string) bool

// readHashes records each file's content as of the model's last read_file (or
// its own last write). Writes compare against it, so the model can't change a
// file it hasn't seen, or one that changed after it looked.
var readHashes = map[string][sha256.Size]byte{}

func checkSeen(path, display string, current []byte) error {
	seen, ok := readHashes[path]
	if !ok {
		return fmt.Errorf("you haven't read %s yet; use read_file before changing it", display)
	}
	if seen != sha256.Sum256(current) {
		return fmt.Errorf("%s has changed since you last read it; read it again before changing it", display)
	}
	return nil
}

func editFile(ctx context.Context, input json.RawMessage, confirm confirmFunc) (string, error) {
	var args struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	switch {
	case args.Path == "":
		return "", fmt.Errorf("path is required")
	case args.OldString == "":
		return "", fmt.Errorf("old_string is empty; use write_file to create a file")
	case args.OldString == args.NewString:
		return "", fmt.Errorf("old_string and new_string are the same")
	}

	path, err := resolvePath(args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := checkSeen(path, args.Path, data); err != nil {
		return "", err
	}

	content := string(data)
	switch n := strings.Count(content, args.OldString); n {
	case 0:
		return "", fmt.Errorf("old_string was not found in %s. It must match the file exactly, "+
			"including whitespace, and must not include read_file's line-number prefixes", args.Path)
	case 1:
	default:
		return "", fmt.Errorf("old_string appears %d times in %s; include more surrounding lines "+
			"so it matches exactly once", n, args.Path)
	}
	updated := strings.Replace(content, args.OldString, args.NewString, 1)

	if err := applyChange(ctx, path, "Edit "+args.Path, content, updated, info.Mode().Perm(), confirm); err != nil {
		return "", err
	}
	return "Edited " + args.Path, nil
}

func writeFile(ctx context.Context, input json.RawMessage, confirm confirmFunc) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
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

	perm := fs.FileMode(0o644)
	exists := false
	var old string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := checkSeen(path, args.Path, data); err != nil {
			return "", err
		}
		if string(data) == args.Content {
			return "", fmt.Errorf("%s already has exactly this content", args.Path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		perm, exists, old = info.Mode().Perm(), true, string(data)
	case errors.Is(err, fs.ErrNotExist):
		// New file.
	default:
		return "", err // e.g. the path is a directory
	}

	header, verb := "Create "+args.Path, "Created"
	if exists {
		header, verb = "Overwrite "+args.Path, "Overwrote"
	}
	if err := applyChange(ctx, path, header, old, args.Content, perm, confirm); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s (%d lines)", verb, args.Path, len(splitLines(args.Content))), nil
}

// applyChange shows the user what's about to change, asks for approval, and
// writes the file. Declining is reported to the model as an error so it knows
// nothing happened.
func applyChange(ctx context.Context, path, header, oldText, newText string, perm fs.FileMode, confirm confirmFunc) error {
	fmt.Printf("\n%s\n%s", header, changePreview(oldText, newText))
	if !confirm(ctx, "Apply this change?") {
		return errors.New("the user declined this change, so the file was not modified. " +
			"Ask them what they want instead of retrying the same change")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeAtomic(path, []byte(newText), perm); err != nil {
		return err
	}
	readHashes[path] = sha256.Sum256([]byte(newText)) // its own write doesn't need a re-read
	return nil
}

// writeAtomic writes to a temporary file and renames it into place, so an
// interrupted write can't leave a half-written source file behind.
func writeAtomic(path string, data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".trubo-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename has happened
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// changePreview shows the lines that differ between oldText and newText, with
// a little unchanged context and line numbers. It finds the changed block by
// trimming the lines the two share at the start and end. That's exact for a
// single edit; a rewrite with scattered changes shows as one large block.
func changePreview(oldText, newText string) string {
	a, b := splitLines(oldText), splitLines(newText)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}

	var out strings.Builder
	if pre == len(a) && pre == len(b) {
		out.WriteString("  (only the newline at the end of the file changes)\n")
		return out.String()
	}
	for i := max(pre-previewContext, 0); i < pre; i++ {
		fmt.Fprintf(&out, "%6d   %s\n", i+1, a[i])
	}
	writePreviewSide(&out, "-", a[pre:len(a)-suf], pre)
	writePreviewSide(&out, "+", b[pre:len(b)-suf], pre)
	for i := len(a) - suf; i < min(len(a)-suf+previewContext, len(a)); i++ {
		fmt.Fprintf(&out, "%6d   %s\n", i+1, a[i])
	}
	return out.String()
}

func writePreviewSide(out *strings.Builder, mark string, lines []string, start int) {
	for i, line := range lines {
		if i == maxPreviewLines {
			fmt.Fprintf(out, "       %s ... %d more lines\n", mark, len(lines)-i)
			return
		}
		fmt.Fprintf(out, "%6d %s %s\n", start+i+1, mark, line)
	}
}
