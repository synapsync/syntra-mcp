package toolclient

import (
	"encoding/json"
	"fmt"
	"io"

	"google.golang.org/protobuf/proto"
	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
)

// ReadContentInputWithCorrections reports client-side corrections without changing
// the strict server decoder or the canonical authored-content encoding.
func ReadContentInputWithCorrections(r io.Reader, target proto.Message, report func(string)) error {
	b, e := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if e != nil || len(b) > maxResponseBytes {
		return ci.ErrWire
	}
	return decodeClientContent(b, target, report)
}

// rejectDuplicates validates raw bytes before any lossy JSON map conversion.
func rejectDuplicates(b []byte, target proto.Message) error {
	if ValidateRawObject(b, maxResponseBytes) == nil {
		return nil
	}
	if json.Valid(b) {
		if err := diagnoseJSON(b, target.ProtoReflect().Descriptor(), "request"); err != nil {
			return err
		}
	}
	return ci.ErrWire
}

// moveMisplacedRevision only moves recognized concurrency fields. Ambiguous
// placement fails closed; authored content itself is never interpreted here.
func moveMisplacedRevision(root, content map[string]json.RawMessage, contentKey, expectationKey, key string) (string, bool, error) {
	rev, exists := content[key]
	if !exists {
		return "", false, nil
	}
	destination := "top level"
	if _, has := root[expectationKey]; has {
		var expectation map[string]json.RawMessage
		if json.Unmarshal(root[expectationKey], &expectation) != nil || expectation == nil {
			return "", false, fmt.Errorf("%w: %s must be an object", ci.ErrWire, expectationKey)
		}
		if _, exists = expectation[key]; exists {
			return "", false, fmt.Errorf("%w: duplicate %s in %s and %s", ci.ErrWire, key, contentKey, expectationKey)
		}
		expectation[key] = rev
		raw, err := json.Marshal(expectation)
		if err != nil {
			return "", false, ci.ErrWire
		}
		root[expectationKey] = raw
		destination = expectationKey
	} else {
		if key != "expectedThreadRevision" || contentKey != "content" {
			return "", false, fmt.Errorf("%w: %s belongs in %s", ci.ErrWire, key, expectationKey)
		}
		if _, exists = root[key]; exists {
			return "", false, fmt.Errorf("%w: duplicate %s in content and top level", ci.ErrWire, key)
		}
		root[key] = rev
	}
	delete(content, key)
	return destination, true, nil
}

func normalizeRoot(b []byte, contentKey, expectationKey string, report func(string)) ([]byte, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil {
		return nil, ci.ErrWire
	}
	var content map[string]json.RawMessage
	if json.Unmarshal(root[contentKey], &content) != nil || content == nil {
		return b, nil
	}
	changed := false
	for _, key := range []string{"expectedThreadRevision", "expectedPreparedMessageRevision"} {
		destination, moved, err := moveMisplacedRevision(root, content, contentKey, expectationKey, key)
		if err != nil {
			return nil, err
		}
		if moved {
			changed = true
			if report != nil {
				report("moved " + contentKey + "." + key + " to " + destination)
			}
		}
	}
	if !changed {
		return b, nil
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return nil, ci.ErrWire
	}
	root[contentKey] = raw
	raw, err = json.Marshal(root)
	if err != nil {
		return nil, ci.ErrWire
	}
	return raw, nil
}

func decodeClientContent(b []byte, target proto.Message, report func(string)) error {
	contentKey, expectationKey := "", ""
	switch target.(type) {
	case *v1.PrepareContentRequest:
		contentKey, expectationKey = "content", "update"
	case *v1.LinkContentRequest:
		contentKey, expectationKey = "linkedContent", "current"
	}
	if contentKey != "" {
		if err := rejectDuplicates(b, target); err != nil {
			return err
		}
		var err error
		b, err = normalizeRoot(b, contentKey, expectationKey, report)
		if err != nil {
			return err
		}
	}
	if err := ci.DecodeStrictJSON(b, target); err != nil {
		if detail := diagnoseJSON(b, target.ProtoReflect().Descriptor(), "request"); detail != nil {
			return detail
		}
		return err
	}
	if q, ok := target.(*v1.PrepareContentRequest); ok && q.Content != nil {
		if err := checkPreparePlaceholders(q); err != nil {
			return err
		}
	}
	return nil
}
