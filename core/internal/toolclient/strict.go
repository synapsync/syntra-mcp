package toolclient

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// ValidateRawObject is the shared lossless JSON gate for inline MCP arguments
// and protocol frames. It precedes any decoder that could replace Unicode,
// accept duplicate keys, or erase null presence. Domain oneofs/known keys remain
// the frozen content decoder's responsibility.
func ValidateRawObject(raw []byte, maxBytes int) error {
	if len(raw) > maxBytes || !utf8.Valid(raw) || !json.Valid(raw) || !validEscapedUnicode(raw) {
		return ErrProfile
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrProfile
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if uniqueJSON(d) != nil {
		return ErrProfile
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrProfile
	}
	return nil
}
