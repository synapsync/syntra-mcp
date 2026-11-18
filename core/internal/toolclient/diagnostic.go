package toolclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
)

// diagnoseJSON only describes names and types, never values. It is client-side;
// the server's strict wire decoder remains unchanged.
func diagnoseJSON(raw []byte, desc protoreflect.MessageDescriptor, path string) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	tok, e := d.Token()
	if e != nil || tok != json.Delim('{') {
		return fmt.Errorf("%w: %s must be an object", ci.ErrWire, path)
	}
	seen := map[string]bool{}
	oneofs := map[protoreflect.FullName]bool{}
	fields := map[string]protoreflect.FieldDescriptor{}
	names := []string{}
	for i := 0; i < desc.Fields().Len(); i++ {
		f := desc.Fields().Get(i)
		fields[f.JSONName()] = f
		names = append(names, f.JSONName())
	}
	sort.Strings(names)
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return ci.ErrWire
		}
		name, ok := k.(string)
		if !ok {
			return ci.ErrWire
		}
		if len(name) > 80 || strings.ContainsAny(name, "\n\r\t") {
			return fmt.Errorf("%w: invalid key at %s", ci.ErrWire, path)
		}
		if seen[name] {
			return fmt.Errorf("%w: duplicate key %s at %s", ci.ErrWire, name, path)
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return ci.ErrWire
		}
		f, ok := fields[name]
		if !ok {
			hint := "it is not allowed in the request"
			locations := map[string]string{"requestId": "the request top level", "content": "the request top level", "linkedContent": "the link request top level", "expectedTicketRevision": "the prepare request top level or current/update expectation", "expectedDraftRevision": "the current/update expectation", "expectedAuthorUserId": "content or the publish request top level", "workspaceId": "content or the publish request top level", "projectId": "content", "existingTicketId": "content", "title": "content", "summary": "content", "contextMarkdown": "content", "destination": "content", "linkedMemberId": "content.destination", "unlinkedLabel": "content.destination", "originalDestinationIntent": "content", "current": "the link or publish request top level", "update": "the prepare request top level", "clientReported": "the request top level", "ticketId": "current/update", "draftId": "current/update"}
			if where, found := locations[name]; found {
				hint = "it belongs in " + where
			}
			return fmt.Errorf("%w: unknown or misplaced key %s at %s (%s; allowed keys: %s)", ci.ErrWire, name, path, hint, strings.Join(names, ", "))
		}
		if group := f.ContainingOneof(); group != nil && !group.IsSynthetic() {
			if oneofs[group.FullName()] {
				return fmt.Errorf("%w: %s.%s conflicts with another field in the same choice; select only one", ci.ErrWire, path, name)
			}
			oneofs[group.FullName()] = true
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%w: %s.%s cannot be null", ci.ErrWire, path, name)
		}
		if f.Kind() == protoreflect.MessageKind {
			if e := diagnoseJSON(value, f.Message(), name); e != nil {
				return e
			}
			continue
		}
		var v any
		if json.Unmarshal(value, &v) != nil {
			return ci.ErrWire
		}
		valid := false
		switch f.Kind() {
		case protoreflect.StringKind, protoreflect.BytesKind:
			_, valid = v.(string)
		case protoreflect.BoolKind:
			_, valid = v.(bool)
		case protoreflect.EnumKind:
			switch v.(type) {
			case string, float64:
				valid = true
			}
		default:
			_, valid = v.(float64)
		}
		if valid && f.Kind() == protoreflect.BytesKind {
			value := v.(string)
			if _, err := base64.StdEncoding.DecodeString(value); err != nil {
				return fmt.Errorf("%w: %s.%s must be a base64-encoded byte string", ci.ErrWire, path, name)
			}
		}
		if !valid {
			return fmt.Errorf("%w: %s.%s has the wrong JSON type (expected %s)", ci.ErrWire, path, name, f.Kind())
		}
	}
	return nil
}

func checkPlaceholders(c *v1.AuthoredContent) error {
	if c == nil {
		return nil
	}
	for _, f := range []struct{ name, value string }{{"title", c.Title}, {"originalDestinationIntent", c.OriginalDestinationIntent}, {"summary", c.Summary}, {"contextMarkdown", c.ContextMarkdown}, {"destination.unlinkedLabel", c.GetDestination().GetUnlinkedLabel()}} {
		if strings.Contains(f.value, "TODO: SYNTRA") {
			return fmt.Errorf("%w: content.%s contains a TODO: SYNTRA placeholder; replace it before preparing", ci.ErrWire, f.name)
		}
	}
	return nil
}

func checkPreparePlaceholders(q *v1.PrepareContentRequest) error {
	if q == nil {
		return nil
	}
	var scan func(protoreflect.Message, string) error
	scan = func(m protoreflect.Message, path string) error {
		fields := m.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			if !m.Has(f) {
				continue
			}
			v := m.Get(f)
			field := string(f.JSONName())
			if path != "" {
				field = path + "." + field
			}
			if f.Kind() == protoreflect.MessageKind {
				if err := scan(v.Message(), field); err != nil {
					return err
				}
			} else if f.Kind() == protoreflect.StringKind && strings.Contains(v.String(), "TODO: SYNTRA") {
				return fmt.Errorf("%w: %s contains a TODO: SYNTRA placeholder; replace it before preparing", ci.ErrWire, field)
			}
		}
		return nil
	}
	return scan(q.ProtoReflect(), "")
}
