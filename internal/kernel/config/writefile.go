package config

import (
	"os"
	"path/filepath"
)

// ErrSymlink は書き先がシンボリックリンクのとき(書かない)。
type ErrSymlink struct{ Path string }

func (e *ErrSymlink) Error() string { return filepath.Base(e.Path) + " がシンボリックリンクです: " + e.Path }

// WriteFile は設定のファイル f を body で置き換える(一時ファイルに書いて rename。権限は今のファイルのまま。末尾に改行を足す)。
func WriteFile(f, body string) error {
	if st, err := os.Lstat(f); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return &ErrSymlink{Path: f}
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), filepath.Base(f)+".")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(f); err == nil {
		mode = st.Mode().Perm()
	}
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f)
}

