package toolclient

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	v1 "syntra.local/mcp/gen/syntra/v1"
)

func TestUnsafeDiagnosticKeys(t *testing.T) {
	cases := [][]byte{[]byte(`\u001b[31mX`), []byte(`bad\nname`), []byte(strings.Repeat("a", 65)), {0xff, 'x'}}
	for _, key := range cases {
		raw := append([]byte(`{"content":{"`), key...)
		raw = append(raw, []byte(`":"secret"}}`)...)
		var q v1.PrepareContentRequest
		err := ReadContentInput(bytes.NewReader(raw), &q)
		if err == nil || !strings.Contains(err.Error(), "an invalid key") || strings.ContainsAny(err.Error(), "\x1b\n\r") || strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "request.content") {
			t.Fatalf("unsafe key diagnostic: %q", err)
		}
	}
}
func TestNestedDiagnosticPathsAndSuggestion(t *testing.T) {
	cases := []struct {
		raw, want string
		target    interface{ ProtoReflect() protoreflect.Message }
	}{
		{`{"content":{"destination":{"unknowne":"secret"}}}`, "request.content.destination", &v1.PrepareContentRequest{}},
		{`{"update":{"preparedMessageId":null}}`, "request.update.preparedMessageId", &v1.PrepareContentRequest{}},
		{`{"linkedContent":{"destination":{"linkedMemberId":null}}}`, "request.linkedContent.destination.linkedMemberId", &v1.LinkContentRequest{}},
		{`{"content":{"summry":"value"}}`, "did you mean summary?", &v1.PrepareContentRequest{}},
	}
	for _, c := range cases {
		err := ReadContentInput(strings.NewReader(c.raw), c.target)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: %v", c.want, err)
		}
	}
}
func TestDescriptorLocationsCoverEveryField(t *testing.T) {
	for _, root := range []protoreflect.MessageDescriptor{(&v1.PrepareContentRequest{}).ProtoReflect().Descriptor(), (&v1.LinkContentRequest{}).ProtoReflect().Descriptor(), (&v1.PublishContentRequest{}).ProtoReflect().Descriptor(), (&v1.GetContentSnapshotRequest{}).ProtoReflect().Descriptor()} {
		locations := descriptorLocations(root)
		var walk func(protoreflect.MessageDescriptor, string, map[protoreflect.FullName]bool)
		walk = func(d protoreflect.MessageDescriptor, path string, stack map[protoreflect.FullName]bool) {
			if stack[d.FullName()] {
				return
			}
			stack[d.FullName()] = true
			defer delete(stack, d.FullName())
			for i := 0; i < d.Fields().Len(); i++ {
				f := d.Fields().Get(i)
				full := path + "." + f.JSONName()
				found := false
				for _, got := range locations[f.JSONName()] {
					if got == full {
						found = true
					}
				}
				if !found {
					t.Errorf("descriptor field not covered: %s", full)
				}
				if f.Kind() == protoreflect.MessageKind && !f.IsMap() && !f.IsList() {
					walk(f.Message(), full, stack)
				}
			}
		}
		walk(root, "request", map[protoreflect.FullName]bool{})
	}
}
func TestNormalizationHelpers(t *testing.T) {
	q := &v1.PrepareContentRequest{}
	same := []byte(" { \"content\" : { \"summary\": \"x\" } } ")
	got, err := normalizeRoot(same, "content", "update", nil)
	if err != nil || !bytes.Equal(got, same) {
		t.Fatalf("unchanged bytes: %v", err)
	}
	if err = rejectDuplicates([]byte(`{"content":{"summary":"x","summary":"y"}}`), q); err == nil || !strings.Contains(err.Error(), "duplicate key summary") {
		t.Fatal(err)
	}
	root := map[string]json.RawMessage{}
	content := map[string]json.RawMessage{"expectedThreadRevision": json.RawMessage(`"revision"`)}
	where, moved, err := moveMisplacedRevision(root, content, "content", "update", "expectedThreadRevision")
	if err != nil || !moved || where != "top level" || string(root["expectedThreadRevision"]) != `"revision"` {
		t.Fatal(where, moved, err)
	}
	root = map[string]json.RawMessage{"update": json.RawMessage(`{"preparedMessageId":"id"}`)}
	content = map[string]json.RawMessage{"expectedPreparedMessageRevision": json.RawMessage(`"revision"`)}
	where, moved, err = moveMisplacedRevision(root, content, "content", "update", "expectedPreparedMessageRevision")
	if err != nil || !moved || where != "update" || !bytes.Contains(root["update"], []byte(`"expectedPreparedMessageRevision"`)) {
		t.Fatal(where, moved, err)
	}
	content["expectedPreparedMessageRevision"] = json.RawMessage(`"duplicate"`)
	if _, _, err = moveMisplacedRevision(root, content, "content", "update", "expectedPreparedMessageRevision"); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal(err)
	}
	if got := closestField("SuMmRy", []string{"summary", "title"}); got != "summary" {
		t.Fatal(got)
	}
	if got := closestField("completely-unrelated", []string{"summary"}); got != "" {
		t.Fatal(got)
	}
}

func TestOtherDiagnosticTokensCannotInjectControlBytes(t *testing.T) {
	for _, s := range []string{"\x1b[31mX", "line\nbreak", strings.Repeat("x", 65), string([]byte{0xff})} {
		err := (&NotFound{Kind: "team", Query: s, Scope: "project", ScopeID: "11111111-1111-4111-8111-111111111111"}).Error()
		if strings.ContainsAny(err, "\x1b\n\r") || strings.Contains(err, s) {
			t.Fatalf("unsafe error %q", err)
		}
	}
}
