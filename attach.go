package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/api/chat/v1"
)

// file is something in a message that /open and /save can act on.
type file struct {
	label string
	media string // Chat media resource name, for uploaded files
	url   string // browser URL, for Drive files and GIFs
}

func filesOf(m *chat.Message) []file {
	out := make([]file, 0, len(m.Attachment)+len(m.AttachedGifs))
	for _, a := range m.Attachment {
		f := file{label: cmp.Or(a.ContentName, a.ContentType, "attachment")}
		switch {
		case a.AttachmentDataRef != nil:
			f.media = a.AttachmentDataRef.ResourceName
		case a.DriveDataRef != nil:
			f.url = "https://drive.google.com/open?id=" + a.DriveDataRef.DriveFileId
		default:
			f.url = a.DownloadUri
		}
		out = append(out, f)
	}
	for _, g := range m.AttachedGifs {
		out = append(out, file{label: "gif", url: g.Uri})
	}
	return out
}

func threadFiles(t *thread) []file {
	out := make([]file, 0, len(t.msgs))
	for _, m := range t.msgs {
		out = append(out, filesOf(m)...)
	}
	return out
}

// downloadsDir is where /save puts files.
func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	if d := filepath.Join(home, "Downloads"); isDir(d) {
		return d
	}
	return home
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// saveFile downloads an uploaded file into dir and returns its path.
func (c *client) saveFile(ctx context.Context, f file, dir string) (string, error) {
	resp, err := c.svc.Media.Download(f.media).Context(ctx).Download()
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: %s", f.label, resp.Status)
	}
	out, path, err := createUnique(dir, safeName(f.label))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		_ = os.Remove(path) // a partial download is useless
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	quarantine(path)
	return path, nil
}

// safeName keeps a sender-chosen name from escaping the target directory.
func safeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == "/" || name == "" {
		return "attachment"
	}
	return name
}

// createUnique creates dir/name, or "name (2).ext" and so on when it exists,
// and returns the file and its path. Files open through an os.Root, so a
// name can't reach outside dir even if safeName misses a trick.
func createUnique(dir, name string) (*os.File, string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s (%d)%s", base, i, ext)
		}
		f, err := root.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, os.ErrExist) {
			return f, filepath.Join(dir, n), err
		}
	}
	return nil, "", fmt.Errorf("no free name for %s in %s", name, dir)
}

// quarantine marks a downloaded file the way browsers do, so macOS
// Gatekeeper checks it before it runs. Files come from other people.
func quarantine(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	v := fmt.Sprintf("0081;%x;mutter;", time.Now().Unix())
	_ = unix.Setxattr(path, "com.apple.quarantine", []byte(v), 0)
}

// markUnread moves the space's read state to just before at, so everything
// from at onward is unread on every device.
func (c *client) markUnread(ctx context.Context, space, at string) (string, error) {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return "", err
	}
	before := t.Add(-time.Microsecond).UTC().Format(time.RFC3339Nano)
	_, err = c.svc.Users.Spaces.UpdateSpaceReadState(readStateName(space), &chat.SpaceReadState{
		LastReadTime: before,
	}).UpdateMask("lastReadTime").Context(ctx).Do()
	return before, err
}
