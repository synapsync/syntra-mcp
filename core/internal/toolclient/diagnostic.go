package toolclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
	ci "syntra.local/mcp/internal/contentintegrity"
	"syntra.local/mcp/internal/contentintegrity/actionable"
)

// diagnoseJSON only describes names and types, never values. It is client-side;
// the server's strict wire decoder remains unchanged.
func diagnoseJSON(raw []byte, desc protoreflect.MessageDescriptor, path string) error {
	return diagnoseObject(raw, desc, path, descriptorLocations(desc))
}
func diagnoseObject(raw []byte, desc protoreflect.MessageDescriptor, path string, locations map[string][]string) error {
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
		if f.JSONName() == "summary" {
			continue
		}
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
		if name == "summary" {
			return actionable.New(actionable.MessageHasNoTitle)
		}
		if !safeDiagnosticToken(name) {
			return fmt.Errorf("%w: an invalid key at %s", ci.ErrWire, path)
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
			hint := "it is not allowed in this request"
			if paths := locations[name]; len(paths) > 0 {
				hint = "it belongs at " + strings.Join(paths, ", ")
			}
			suggestion := ""
			if candidate := closestField(name, names); candidate != "" {
				suggestion = "; did you mean " + candidate + "?"
			}
			return fmt.Errorf("%w: unknown or misplaced key %s at %s (%s%s; allowed keys: %s)", ci.ErrWire, name, path, hint, suggestion, strings.Join(names, ", "))
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
		if f.IsList() {
			// Only repeated strings (a recipient set) exist; each item must be a string.
			var items []any
			if json.Unmarshal(value, &items) != nil {
				return fmt.Errorf("%w: %s.%s has the wrong JSON type (expected an array of %s)", ci.ErrWire, path, name, f.Kind())
			}
			for _, item := range items {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("%w: %s.%s must contain only strings", ci.ErrWire, path, name)
				}
			}
			continue
		}
		if f.Kind() == protoreflect.MessageKind {
			if e := diagnoseObject(value, f.Message(), path+"."+name, locations); e != nil {
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

// diagnosticToken restricts all untrusted identifiers echoed in errors to a
// small ASCII alphabet. It also bounds output and suggestion work.
func safeDiagnosticToken(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// IsSafeDiagnosticToken reports whether an identifier can appear in errors.
func IsSafeDiagnosticToken(s string) bool { return safeDiagnosticToken(s) }

// SafeDiagnosticToken is for other CLI error surfaces; never use it to alter data.
func SafeDiagnosticToken(s string) string {
	if !safeDiagnosticToken(s) {
		return "an invalid value"
	}
	return s
}

type locationEntry struct {
	once      sync.Once
	locations map[string][]string
}

var locationsCache sync.Map // protoreflect.FullName -> *locationEntry
func descriptorLocations(root protoreflect.MessageDescriptor) map[string][]string {
	cached, _ := locationsCache.LoadOrStore(root.FullName(), &locationEntry{})
	entry := cached.(*locationEntry)
	entry.once.Do(func() {
		locations := map[string][]string{}
		var walk func(protoreflect.MessageDescriptor, string, map[protoreflect.FullName]bool)
		walk = func(d protoreflect.MessageDescriptor, path string, ancestors map[protoreflect.FullName]bool) {
			if ancestors[d.FullName()] {
				return
			}
			ancestors[d.FullName()] = true
			defer delete(ancestors, d.FullName())
			for i := 0; i < d.Fields().Len(); i++ {
				f := d.Fields().Get(i)
				key := f.JSONName()
				if key == "summary" {
					continue
				}
				fieldPath := path + "." + key
				locations[key] = append(locations[key], fieldPath)
				if f.Kind() == protoreflect.MessageKind && !f.IsMap() && !f.IsList() {
					walk(f.Message(), fieldPath, ancestors)
				}
			}
		}
		walk(root, "request", map[protoreflect.FullName]bool{})
		for _, paths := range locations {
			sort.Strings(paths)
		}
		entry.locations = locations
	})
	return entry.locations
}

func closestField(name string, candidates []string) string {
	if !safeDiagnosticToken(name) {
		return ""
	}
	best := ""
	distance := 3
	for _, candidate := range candidates {
		d := diagnosticDistance(strings.ToLower(name), strings.ToLower(candidate))
		if d < distance {
			distance = d
			best = candidate
		}
	}
	return best
}
func diagnosticDistance(a, b string) int {
	if len(a) > 64 || len(b) > 64 {
		return 65
	}
	row := make([]int, len(b)+1)
	for i := range row {
		row[i] = i
	}
	for i := 1; i <= len(a); i++ {
		previous := row[0]
		row[0] = i
		for j := 1; j <= len(b); j++ {
			old := row[j]
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			v := row[j] + 1
			if row[j-1]+1 < v {
				v = row[j-1] + 1
			}
			if previous+cost < v {
				v = previous + cost
			}
			row[j] = v
			previous = old
		}
	}
	return row[len(b)]
}

func checkPlaceholders(c *v1.AuthoredContent) error {
	if c == nil {
		return nil
	}
	for _, f := range []struct{ name, value string }{{"title", c.Title}, {"originalDestinationIntent", c.OriginalDestinationIntent}, {"contextMarkdown", c.ContextMarkdown}, {"destination.unlinkedLabel", c.GetDestination().GetUnlinkedLabel()}} {
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
