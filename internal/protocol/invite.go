package protocol

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Invite struct {
	Hub     string `json:"h"`
	Port    int    `json:"p"`
	Node    string `json:"n"`
	Code    string `json:"k"`
	Expires int64  `json:"e"`
	CA      []byte `json:"c"`
}

type JoinCode struct {
	Hub  string `json:"h"`
	Port int    `json:"p"`
	CA   []byte `json:"c"`
}

func (i JoinCode) Encode() string {
	b, _ := json.Marshal(i)
	return "DG2." + base64.RawURLEncoding.EncodeToString(b)
}

func ParseJoinCode(input string) (JoinCode, error) {
	var i JoinCode
	invalid := errors.New("连接码无效，请完整复制 DG2 开头的连接码")
	if !decodeCode(input, "DG2.", &i) || !validAddress(i.Hub, i.Port, i.CA) {
		return JoinCode{}, invalid
	}
	return i, nil
}

func ValidPairKey(input string) bool {
	b, e := base64.RawURLEncoding.Strict().DecodeString(input)
	return e == nil && len(b) == 32
}

func decodeCode(input, prefix string, v any) bool {
	raw, ok := strings.CutPrefix(strings.TrimSpace(input), prefix)
	if !ok || len(raw) > 8000 {
		return false
	}
	b, e := base64.RawURLEncoding.Strict().DecodeString(raw)
	if e != nil {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v) == nil && d.Decode(new(any)) == io.EOF
}

func validAddress(origin string, port int, der []byte) bool {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || origin != "https://"+u.Host || strings.Contains(u.Hostname(), "%") || port < 1 || port > 65535 {
		return false
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	ca, e := x509.ParseCertificate(der)
	return e == nil && ca.IsCA && ca.CheckSignatureFrom(ca) == nil
}

func (i Invite) Encode() string {
	b, _ := json.Marshal(i)
	return "DG1." + base64.RawURLEncoding.EncodeToString(b)
}

func ParseInvite(input string, now time.Time) (Invite, error) {
	invalid := errors.New("接入码格式无效，请完整复制 DG1 开头的接入码")
	var i Invite
	if !decodeCode(input, "DG1.", &i) || !ID(i.Node) || !ValidPairKey(i.Code) || !validAddress(i.Hub, i.Port, i.CA) {
		return Invite{}, invalid
	}
	if now.Unix() >= i.Expires {
		return Invite{}, errors.New("接入码已过期，请让主机重新生成")
	}
	return i, nil
}

// PairTag is only a visual out-of-band comparison; the approval request still
// carries the complete certificate fingerprint and the Hub validates it.
func PairTag(fingerprint string) string {
	if len(fingerprint) != 64 {
		return ""
	}
	return strings.ToUpper(fingerprint[:4] + "-" + fingerprint[4:8] + "-" + fingerprint[8:12])
}
