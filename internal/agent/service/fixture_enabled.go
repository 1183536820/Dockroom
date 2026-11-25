//go:build dockroom_fixture

package service

import (
	"context"
	"os"
)

// This hook is only linked into the independent fixture agent binary.
func fixtureCheckpoint(ctx context.Context, phase string) {
	if os.Getenv("DOCKROOM_FIXTURE_PAUSE") != phase {
		return
	}
	marker := os.Getenv("DOCKROOM_FIXTURE_MARKER")
	if marker == "" {
		return
	}
	f, e := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return
	}
	f.Close()
	<-ctx.Done()
}
