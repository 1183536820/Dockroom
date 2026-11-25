package fs

import (
	"io"
	"os"
)

func DiskSource(dir, name string) (*os.File, error)               { return nil, ErrUnsafe }
func DiskReceive(dir, name string, r io.Reader, size int64) error { return ErrUnsafe }
