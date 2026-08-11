package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

func (store *Store) Lock(ctx context.Context) (func() error, error) {
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return nil, fmt.Errorf("create JDK store: %w", err)
	}
	path := filepath.Join(store.root, "operation.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open operation lock: %w", err)
	}
	if err := lockFile(ctx, file); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire operation lock: %w", err), file.Close())
	}
	var once sync.Once
	var unlockErr error
	return func() error {
		once.Do(func() {
			if err := unlockFile(file); err != nil {
				unlockErr = fmt.Errorf("release operation lock: %w", err)
			}
			if err := file.Close(); unlockErr == nil && err != nil {
				unlockErr = fmt.Errorf("close operation lock: %w", err)
			}
		})
		return unlockErr
	}, nil
}
