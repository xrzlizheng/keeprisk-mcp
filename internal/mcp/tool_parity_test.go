package mcp

import (
	"encoding/json"
	"testing"
)

func TestPythonVsGoParityShape(t *testing.T) {
	p := PythonVsGoParity()
	if p == nil {
		t.Fatal("nil parity")
	}
	tools, ok := p["python_vs_go_tools"].([]map[string]string)
	if !ok || len(tools) < 3 {
		t.Fatalf("python_vs_go_tools: %#v", p["python_vs_go_tools"])
	}
	res, ok := p["resources_parity"].([]map[string]string)
	if !ok {
		t.Fatal("resources_parity missing")
	}
	foundEnterprise := false
	for _, r := range res {
		if r["uri"] == "keeprisk://enterprise/catalog" && r["go"] == "yes" {
			foundEnterprise = true
		}
	}
	if !foundEnterprise {
		t.Fatal("enterprise catalog resource not in parity")
	}
	b, err := json.Marshal(p)
	if err != nil || len(b) < 50 {
		t.Fatalf("marshal parity: %v len=%d", err, len(b))
	}
}