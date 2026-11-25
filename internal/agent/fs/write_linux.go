package fs

import (
	"os"
	"time"
)

const CanWrite = false

func DiskFull(error) bool               { return false }
func CheckSpace(WriteArea, int64) error { return ErrUnsafe }

type WriteArea struct{ Parent, Private *os.File }

func (WriteArea) Close()                                   {}
func PrepareWrite(Root) (string, error)                    { return "", ErrUnsafe }
func (Root) WriteArea([]string, string) (WriteArea, error) { return WriteArea{}, ErrUnsafe }
func (WriteArea) Stage(string, bool) (*os.File, error)     { return nil, ErrUnsafe }
func OpenSource(Root, []string) (*os.File, error)          { return nil, ErrUnsafe }
func OpenFinal(WriteArea, string) (*os.File, error)        { return nil, ErrUnsafe }
func Absent(error) bool                                    { return false }
func Collision(error) bool                                 { return false }
func SetMTime(*os.File, time.Time) error                   { return ErrUnsafe }
func Publish(*os.File, *os.File, string) error             { return ErrUnsafe }
