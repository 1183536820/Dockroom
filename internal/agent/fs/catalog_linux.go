package fs

import "os"

func CatalogDrives() ([]string, error)          { return nil, ErrUnsafe }
func CatalogDirectory(string) (*os.File, error) { return nil, ErrUnsafe }
