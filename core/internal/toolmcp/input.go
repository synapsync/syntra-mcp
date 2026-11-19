// Package toolmcp adapts a bounded local stdio protocol to the shared toolclient.
// Thread text is data, never executable authority or a source of configuration.
package toolmcp

import (
	"encoding/json"
	"errors"
	"syntra.local/mcp/internal/toolclient"
)

const MaxFrameBytes = 2 * 1024 * 1024
const MaxArgumentsBytes = 1024*1024 + 4096
const MaxResultBytes = 1024 * 1024

var errArguments = errors.New("invalid tool arguments")

// Arguments are never reconstructed from a lossy typed SDK decode. Only exact
// outer keys are accepted, then the raw nested request uses the frozen decoder.
func object(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	if toolclient.ValidateRawObject(raw, MaxArgumentsBytes) != nil {
		return nil, errArguments
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, errArguments
	}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
		if _, ok := m[k]; !ok {
			return nil, errArguments
		}
	}
	for _, k := range optional {
		allowed[k] = true
	}
	for k := range m {
		if !allowed[k] {
			return nil, errArguments
		}
	}
	return m, nil
}
func text(m map[string]json.RawMessage, k string, required bool) (string, error) {
	raw, ok := m[k]
	if !ok && !required {
		return "", nil
	}
	var s string
	if !ok || json.Unmarshal(raw, &s) != nil || len(s) > 2048 || required && s == "" {
		return "", errArguments
	}
	return s, nil
}
