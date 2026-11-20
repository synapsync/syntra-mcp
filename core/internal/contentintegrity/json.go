package contentintegrity

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

// DecodeStrictJSON is the raw-byte gate required before protobuf JSON decoding.
// AuthoredContentService invokes this gate before its lossy protobuf JSON codec.
// It rejects aliases/unknown/duplicate keys, null, ambiguous oneofs and invalid
// UTF-8/UTF-16 escapes. Never inspect only an already-decoded Go message.
func DecodeStrictJSON(data []byte, target proto.Message) error {
	switch target.(type) {
	case *v1.PrepareContentRequest, *v1.LinkContentRequest, *v1.PublishContentRequest, *v1.GetContentSnapshotRequest:
	default:
		return ErrWire
	}

	if len(data) > 1048576 || !utf8.Valid(data) || !json.Valid(data) || !validEscapes(data) || target == nil || !target.ProtoReflect().IsValid() {
		return ErrWire
	}
	if e := jsonObject(data, target.ProtoReflect().Descriptor()); e != nil {
		return e
	}
	// Never partially replace a caller's value on failure.
	next := target.ProtoReflect().New().Interface()
	if e := protojson.Unmarshal(data, next); e != nil {
		return ErrWire
	}
	proto.Reset(target)
	proto.Merge(target, next)
	return nil
}
func jsonObject(data []byte, desc protoreflect.MessageDescriptor) error {
	d := json.NewDecoder(bytes.NewReader(data))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return ErrWire
	}
	seen := map[string]bool{}
	oneofs := map[protoreflect.FullName]bool{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return ErrWire
		}
		name, ok := k.(string)
		if !ok || seen[name] {
			return ErrWire
		}
		seen[name] = true
		var f protoreflect.FieldDescriptor
		for i := 0; i < desc.Fields().Len(); i++ {
			candidate := desc.Fields().Get(i)
			if candidate.JSONName() == name {
				f = candidate
				break
			}
		}
		if f == nil || f.IsMap() || (f.IsList() && f.Kind() != protoreflect.StringKind) {
			return ErrWire
		}
		if o := f.ContainingOneof(); o != nil && !o.IsSynthetic() {
			if oneofs[o.FullName()] {
				return ErrWire
			}
			oneofs[o.FullName()] = true
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrWire
		}
		if f.IsList() {
			// Only an array of non-null strings: a recipient set. Anything else is ambiguous.
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) != nil {
				return ErrWire
			}
			for _, item := range items {
				var text string
				if json.Unmarshal(item, &text) != nil {
					return ErrWire
				}
			}
		} else if f.Kind() == protoreflect.MessageKind {
			if e := jsonObject(raw, f.Message()); e != nil {
				return e
			}
		}
	}
	if _, e = d.Token(); e != nil {
		return ErrWire
	}
	if _, e = d.Token(); e != io.EOF {
		return ErrWire
	}
	return nil
}

// encoding/json replaces lone escaped surrogates; inspect them before decoding.
func validEscapes(b []byte) bool {
	quoted := false
	for i := 0; i < len(b); i++ {
		if b[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || b[i] != '\\' {
			continue
		}
		i++
		if b[i] != 'u' {
			continue
		}
		n, e := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
