//go:build !wasip1

package fsutil

func replace(tmp, orig string) error { return renameReplace(tmp, orig) }
