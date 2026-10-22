// Package transfer prepares directory bundles and validates receiver destinations.
package transfer

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const MaxEntries = 10000

type Entry struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Dir  bool   `json:"dir,omitempty"`
	Mode int64  `json:"mode,omitempty"`
}
type Source struct {
	Path    string  `json:"-"`
	Name    string  `json:"name"`
	Size    int64   `json:"size"`
	Archive bool    `json:"archive"`
	Entries []Entry `json:"entries"`
	cleanup string
}

func (s *Source) Close() {
	if s.cleanup != "" {
		_ = os.RemoveAll(s.cleanup)
	}
}
func SafeName(name string) bool {
	return name != "" && name != "." && !strings.ContainsAny(name, "\\:\x00") && !path.IsAbs(name) && path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}
func Prepare(ctx context.Context, source string) (*Source, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("不能分发符号链接，请选择实际文件或目录")
	}
	name := info.Name()
	if !SafeName(name) {
		return nil, errors.New("文件名不兼容")
	}
	s := &Source{Name: name, Path: source, Size: info.Size()}
	if info.Mode().IsRegular() {
		s.Entries = []Entry{{Name: name, Size: info.Size(), Mode: int64(info.Mode().Perm())}}
		return s, nil
	}
	if !info.IsDir() {
		return nil, errors.New("请选择普通文件或目录")
	}
	temp, err := os.MkdirTemp("", "gosilver-bundle-")
	if err != nil {
		return nil, err
	}
	s.cleanup = temp
	success := false
	defer func() {
		if !success {
			s.Close()
		}
	}()
	s.Archive = true
	s.Path = filepath.Join(temp, name+".gosilver.tar")
	f, err := os.Create(s.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	err = filepath.WalkDir(source, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("不支持链接或特殊文件: %s", p)
		}
		rel, e := filepath.Rel(filepath.Dir(source), p)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		if !SafeName(rel) {
			return fmt.Errorf("无效路径: %s", rel)
		}
		if len(s.Entries) >= MaxEntries {
			return fmt.Errorf("目录超过 %d 个条目", MaxEntries)
		}
		entry := Entry{Name: rel, Size: info.Size(), Dir: info.IsDir(), Mode: int64(info.Mode().Perm())}
		if entry.Dir {
			entry.Size = 0
		}
		s.Entries = append(s.Entries, entry)
		h, e := tar.FileInfoHeader(info, "")
		if e != nil {
			return e
		}
		h.Name = rel
		if e = tw.WriteHeader(h); e != nil {
			return e
		}
		if !info.IsDir() {
			in, e := os.Open(p)
			if e != nil {
				return e
			}
			_, e = CopyContext(ctx, tw, in)
			closeErr := in.Close()
			if e != nil {
				return e
			}
			return closeErr
		}
		return nil
	})
	if closeErr := tw.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s.Size = stat.Size()
	success = true
	return s, nil
}
func CopyContext(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, 128<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			written, e := w.Write(buf[:n])
			total += int64(written)
			if e != nil {
				return total, e
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// Resolve rejects symlink components as well as lexical traversal. The receiver
// only permits destinations inside the root it explicitly selected.
func Resolve(root, dir string) (string, error) {
	if root == "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		root = filepath.VolumeName(abs) + string(filepath.Separator)
		dir = abs
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if dir == "" {
		dir = root
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("目标位置超出接收端允许的根目录")
	}
	walk := root
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			walk = filepath.Join(walk, part)
			st, e := os.Lstat(walk)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return "", e
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("目标路径包含符号链接: %s", walk)
			}
		}
	}
	return dir, nil
}
func ValidateEntries(entries []Entry) error {
	if len(entries) == 0 || len(entries) > MaxEntries {
		return errors.New("文件清单为空或过大")
	}
	seen := make(map[string]Entry, len(entries))
	var total int64
	for _, e := range entries {
		_, duplicate := seen[e.Name]
		if !SafeName(e.Name) || e.Size < 0 || e.Size > 1<<50 || duplicate || e.Dir && e.Size != 0 {
			return fmt.Errorf("无效或重复的文件清单项: %s", e.Name)
		}
		total += e.Size
		if total > 1<<50 {
			return errors.New("分发内容超过 1 PiB")
		}
		seen[e.Name] = e
	}
	for _, e := range entries {
		for parent := path.Dir(e.Name); parent != "."; parent = path.Dir(parent) {
			if p, ok := seen[parent]; ok && !p.Dir {
				return fmt.Errorf("文件与目录冲突: %s", parent)
			}
		}
	}
	return nil
}

type Probe struct {
	Ready    bool     `json:"ready"`
	Target   string   `json:"target"`
	Warnings []string `json:"warnings"`
	Bytes    int64    `json:"bytes"`
}

func ProbeTarget(root, dir string, entries []Entry, overwrite bool, extraBytes int64) (Probe, error) {
	p := Probe{Warnings: []string{}}
	if err := ValidateEntries(entries); err != nil {
		return p, err
	}
	target, err := Resolve(root, dir)
	if err != nil {
		return p, err
	}
	p.Target = target
	checked := map[string]bool{}
	for _, e := range entries {
		dest, err := Resolve(root, filepath.Join(target, filepath.FromSlash(e.Name)))
		if err != nil {
			return p, err
		}
		if !e.Dir {
			p.Bytes += e.Size
		}
		st, err := os.Lstat(dest)
		if err == nil {
			if st.IsDir() != e.Dir {
				return p, fmt.Errorf("文件与目录类型冲突: %s", dest)
			}
			if !e.Dir {
				if !overwrite {
					return p, fmt.Errorf("目标文件已存在，尚未允许覆盖: %s", dest)
				}
				f, e := os.OpenFile(dest, os.O_WRONLY, 0)
				if e != nil {
					return p, fmt.Errorf("目标文件不可写: %w", e)
				}
				f.Close()
				p.Warnings = append(p.Warnings, "将覆盖 "+dest)
			}
		} else if !os.IsNotExist(err) {
			return p, err
		}
		parent := filepath.Dir(dest)
		if e.Dir {
			parent = dest
		}
		for {
			st, e := os.Stat(parent)
			if e == nil {
				if !st.IsDir() {
					return p, fmt.Errorf("父路径不是目录: %s", parent)
				}
				break
			}
			if !os.IsNotExist(e) {
				return p, e
			}
			next := filepath.Dir(parent)
			if next == parent {
				return p, e
			}
			parent = next
		}
		if checked[parent] {
			continue
		}
		checked[parent] = true
		// Verify actual write access without truncating any destination file.
		f, e := os.CreateTemp(parent, ".gosilver-probe-*")
		if e != nil {
			return p, fmt.Errorf("目录不可写 %s: %w", parent, e)
		}
		name := f.Name()
		f.Close()
		if e = os.Remove(name); e != nil {
			return p, e
		}
	}
	// Bundles need room for both the downloaded archive and extracted files.
	parent := target
	for {
		if _, e := os.Stat(parent); e == nil {
			break
		}
		parent = filepath.Dir(parent)
	}
	if available, known := freeBytes(parent); known && available < uint64(p.Bytes+extraBytes) {
		return p, fmt.Errorf("磁盘空间不足，需要至少 %d 字节", p.Bytes+extraBytes)
	}
	p.Ready = true
	return p, nil
}
func Extract(ctx context.Context, archive, root, dir string, entries []Entry, overwrite bool) error {
	if _, err := ProbeTarget(root, dir, entries, overwrite, 0); err != nil {
		return err
	}
	target, err := Resolve(root, dir)
	if err != nil {
		return err
	}
	expected := make(map[string]Entry, len(entries))
	for _, e := range entries {
		expected[e.Name] = e
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := strings.TrimSuffix(h.Name, "/")
		entry, ok := expected[name]
		if !ok || !SafeName(name) || h.Size != entry.Size || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir) || (h.Typeflag == tar.TypeDir) != entry.Dir {
			return fmt.Errorf("归档与预探测清单不符: %s", h.Name)
		}
		delete(expected, name)
		dest, e := Resolve(root, filepath.Join(target, filepath.FromSlash(name)))
		if e != nil {
			return e
		}
		if entry.Dir {
			if e = os.MkdirAll(dest, 0755); e != nil {
				return e
			}
			continue
		}
		if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
			return e
		}
		tmp, e := os.CreateTemp(filepath.Dir(dest), ".gosilver-extract-*")
		if e != nil {
			return e
		}
		_, copyErr := CopyContext(ctx, tmp, tr)
		if copyErr == nil {
			copyErr = tmp.Chmod(os.FileMode(entry.Mode) & 0755)
		}
		if copyErr == nil {
			copyErr = tmp.Sync()
		}
		closeErr := tmp.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr == nil {
			if !overwrite {
				if _, e = os.Lstat(dest); e == nil {
					copyErr = fmt.Errorf("目标文件已存在: %s", dest)
				} else if !os.IsNotExist(e) {
					copyErr = e
				}
			}
		}
		if copyErr == nil {
			_, copyErr = Resolve(root, dest)
		}
		if copyErr == nil {
			copyErr = os.Rename(tmp.Name(), dest)
		}
		if copyErr != nil {
			os.Remove(tmp.Name())
			return copyErr
		}
	}
	if len(expected) > 0 {
		return errors.New("归档缺少清单中的文件")
	}
	return nil
}
