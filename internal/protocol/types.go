package protocol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"time"
)

const Major = 1
const Minor = 1

var slug = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func ID(s string) bool { return slug.MatchString(s) }
func Token() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

type Ref struct {
	NodeID string   `json:"nodeId"`
	RootID string   `json:"rootId"`
	Parts  []string `json:"parts"`
}
type Browse struct {
	Ref    Ref    `json:"ref"`
	Filter string `json:"filter,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Input  string `json:"input,omitempty"`
}
type Entry struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	Size    int64     `json:"sizeBytes"`
	MTime   time.Time `json:"mtimeUtc"`
	Version string    `json:"version"`
	Actions []string  `json:"actions"`
}
type Listing struct {
	Entries   []Entry `json:"entries"`
	Native    string  `json:"nativePath"`
	Cursor    string  `json:"nextCursor"`
	ListingID string  `json:"listingId"`
}
type Resolved struct {
	Ref    Ref    `json:"ref"`
	Native string `json:"nativePath"`
}
type Failure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
	Retryable bool   `json:"retryable"`
}

func (f *Failure) Error() string { return f.Code + ": " + f.Message }

type Root struct {
	ID       string `json:"rootId"`
	Label    string `json:"label"`
	Mode     string `json:"mode"`
	CanWrite bool   `json:"canWrite,omitempty"`
}
type AdvertisedRoot struct {
	Root
	Binding string `json:"binding"`
}
type Hello struct {
	Major   int              `json:"major"`
	Minor   int              `json:"minor"`
	Node    string           `json:"nodeId"`
	OS      string           `json:"os"`
	Version string           `json:"version"`
	Volume  string           `json:"volumeCapability"`
	Roots   []AdvertisedRoot `json:"roots"`
}
type Command struct {
	Protocol int       `json:"protocol"`
	ID       string    `json:"requestId"`
	Node     string    `json:"nodeId"`
	Epoch    uint64    `json:"leaseEpoch"`
	Attempt  int       `json:"attempt"`
	Deadline time.Time `json:"deadline"`
	Op       string    `json:"op"`
	Payload  Control   `json:"payload"`
}
type Control struct {
	User string `json:"user"`
	Browse
	Copy *Copy `json:"copy,omitempty"`
}
type Response struct {
	ID    string          `json:"requestId"`
	Value json.RawMessage `json:"result,omitempty"`
	Error *Failure        `json:"error,omitempty"`
}
type Enrollment struct {
	Node string `json:"nodeId"`
	Code string `json:"code"`
	CSR  string `json:"csr"`
}
type Issued struct {
	Certificate string `json:"certificate"`
	Fingerprint string `json:"fingerprint"`
}
