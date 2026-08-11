//go:build !windows

package store

import "os"

func createDirectoryLink(link, target string) error {
	return os.Symlink(target, link)
}
