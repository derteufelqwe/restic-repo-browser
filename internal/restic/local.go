package restic

import (
	"io/fs"
	"path/filepath"
)

// dirSize adds up the size of all files below root (the size of a local repository).
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // file vanished mid-walk (a backup rewrote it)
		}
		total += info.Size()
		return nil
	})
	return total, err
}
