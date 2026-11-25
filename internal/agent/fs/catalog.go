package fs

import "strings"

func catalogParts(s string) ([]string, error) {
	p := strings.Split(s, `\`)
	if len(p) > 128 {
		return nil, ErrUnsafe
	}
	for _, v := range p {
		if !Part(v) {
			return nil, ErrUnsafe
		}
	}
	return p, nil
}
