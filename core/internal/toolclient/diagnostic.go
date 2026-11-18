package toolclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"
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
			hint := ""
			if name == "expectedTicketRevision" && path == "content" {
				hint = "; it belongs at the top level"
			}
			return fmt.Errorf("%w: unknown key %s at %s%s (allowed: %s)", ci.ErrWire, name, path, hint, strings.Join(names, ", "))
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
		if !valid {
			return fmt.Errorf("%w: %s.%s has the wrong JSON type", ci.ErrWire, path, name)
		}
	}
	return nil
}
