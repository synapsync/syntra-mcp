package toolclient

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
func decodeClientContent(b []byte, target proto.Message, report func(string)) error {
	contentKey, expectationKey := "", ""
	switch target.(type) {
	case *v1.PrepareContentRequest:
		contentKey, expectationKey = "content", "update"
	case *v1.LinkContentRequest:
		contentKey, expectationKey = "linkedContent", "current"
	}
	if contentKey != "" {
		if ValidateRawObject(b, maxResponseBytes) != nil {
			if json.Valid(b) {
				if e := diagnoseJSON(b, target.ProtoReflect().Descriptor(), "request"); e != nil {
					return e
				}
			}
			return ci.ErrWire
		}
		var root map[string]json.RawMessage
		if json.Unmarshal(b, &root) != nil {
			return ci.ErrWire
		}
		var content map[string]json.RawMessage
		if json.Unmarshal(root[contentKey], &content) == nil && content != nil {
			changed := false
			for _, key := range []string{"expectedTicketRevision", "expectedDraftRevision"} {
				rev, exists := content[key]
				if !exists {
					continue
				}
				destination := "top level"
				if _, hasExpectation := root[expectationKey]; hasExpectation {
					var expectation map[string]json.RawMessage
					if json.Unmarshal(root[expectationKey], &expectation) != nil || expectation == nil {
						return fmt.Errorf("%w: %s must be an object", ci.ErrWire, expectationKey)
					}
					if _, exists := expectation[key]; exists {
						return fmt.Errorf("%w: duplicate %s in %s and %s", ci.ErrWire, key, contentKey, expectationKey)
					}
					expectation[key] = rev
					raw, err := json.Marshal(expectation)
					if err != nil {
						return ci.ErrWire
					}
					root[expectationKey] = raw
					destination = expectationKey
				} else {
					if key != "expectedTicketRevision" || contentKey != "content" {
						return fmt.Errorf("%w: %s belongs in %s", ci.ErrWire, key, expectationKey)
					}
					if _, exists := root[key]; exists {
						return fmt.Errorf("%w: duplicate %s in content and top level", ci.ErrWire, key)
					}
					root[key] = rev
				}
				delete(content, key)
				changed = true
				if report != nil {
					report("moved " + contentKey + "." + key + " to " + destination)
				}
			}
			if changed {
				raw, err := json.Marshal(content)
				if err != nil {
					return ci.ErrWire
				}
				root[contentKey] = raw
				b, err = json.Marshal(root)
				if err != nil {
					return ci.ErrWire
				}
			}
		}
	}

	if e := ci.DecodeStrictJSON(b, target); e != nil {
		if diagnostic := diagnoseJSON(b, target.ProtoReflect().Descriptor(), "request"); diagnostic != nil {
			return diagnostic
		}
		return e
	}

	if q, ok := target.(*v1.PrepareContentRequest); ok && q.Content != nil {
		for _, s := range []string{q.Content.Title, q.Content.OriginalDestinationIntent, q.Content.Summary, q.Content.ContextMarkdown, q.Content.GetDestination().GetUnlinkedLabel()} {
			if strings.Contains(s, "TODO: SYNTRA") {
				return fmt.Errorf("%w: replace all TODO: SYNTRA placeholders before preparing", ci.ErrWire)
			}
		}
	}
	return nil
}
