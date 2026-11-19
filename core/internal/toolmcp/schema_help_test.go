package toolmcp

import (
	"encoding/json"
	"strings"
	v1 "syntra.local/mcp/gen/syntra/v1"
	"syntra.local/mcp/internal/toolclient"
	"testing"
)

func TestSelfDescribingTools(t *testing.T) {
	for _, name := range []string{"prepare", "link", "publish", "resolve", "thread"} {
		t.Run(name, func(t *testing.T) {
			tool := toolDefinition(name)
			raw, e := json.Marshal(tool.InputSchema)
			if e != nil {
				t.Fatal(e)
			}
			if !strings.Contains(tool.Description, "Example") {
				t.Fatal("missing tool example")
			}
			var schema map[string]any
			if e = json.Unmarshal(raw, &schema); e != nil {
				t.Fatal(e)
			}
			var inspect func(map[string]any, string)
			inspect = func(object map[string]any, path string) {
				props, _ := object["properties"].(map[string]any)
				for key, v := range props {
					p := v.(map[string]any)
					if p["description"] == nil || p["examples"] == nil {
						t.Errorf("%s.%s missing description or example", path, key)
					}
					inspect(p, path+"."+key)
				}
			}
			inspect(schema, name)
			if name == "prepare" && (!strings.Contains(string(raw), "top level") || !strings.Contains(string(raw), "expectedThreadRevision")) {
				t.Fatal("revision placement missing")
			}
		})
	}
}

func TestMCPNestedDiagnosticMatchesCLI(t *testing.T) {
	const request = `{"requestId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","content":{"summary":null}}`
	var q v1.PrepareContentRequest
	err := toolclient.ReadContentInput(strings.NewReader(request), &q)
	if err == nil {
		t.Fatal("CLI accepted null")
	}
	_, mcpErr := decodeArgs("prepare", json.RawMessage(`{"request":`+request+`}`))
	if mcpErr == nil || mcpErr.Error() != err.Error() {
		t.Fatalf("CLI %v; MCP %v", err, mcpErr)
	}
}
