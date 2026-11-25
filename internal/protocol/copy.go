package protocol

import "time"

type Source struct {
	Ref     Ref    `json:"ref"`
	Version string `json:"version"`
}
type PlanInput struct {
	Action   string   `json:"action"`
	Sources  []Source `json:"sources"`
	Dest     Ref      `json:"destination"`
	Conflict string   `json:"conflictPolicy"`
}
type Copy struct {
	Version string    `json:"version,omitempty"`
	ID      string    `json:"id,omitempty"`
	Name    string    `json:"name,omitempty"`
	Parent  string    `json:"parent,omitempty"`
	Size    int64     `json:"size,omitempty"`
	Offset  int64     `json:"offset,omitempty"`
	Length  int64     `json:"length,omitempty"`
	Token   string    `json:"token,omitempty"`
	Digest  string    `json:"digest,omitempty"`
	MTime   time.Time `json:"mtime,omitempty"`
}
type CopyResult struct {
	Native string    `json:"nativePath,omitempty"`
	Parent string    `json:"parent,omitempty"`
	Size   int64     `json:"sizeBytes,omitempty"`
	Offset int64     `json:"offset,omitempty"`
	Digest string    `json:"digest,omitempty"`
	State  string    `json:"state,omitempty"`
	MTime  time.Time `json:"mtime,omitempty"`
}
