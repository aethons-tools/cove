package sessionevents

import "testing"

func TestDeriveIndexToolUse(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"assistant","session_id":"S1","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]}}`))
	if ix.Type != "assistant" || ix.ToolName != "Bash" || ix.ClaudeSessionID != "S1" {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexToolResultError(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"boom","is_error":true}]}}`))
	if ix.Type != "user" || !ix.IsError {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexStringContent(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"user","session_id":"S2","message":{"role":"user","content":"do the thing"}}`))
	if ix.Type != "user" || ix.ClaudeSessionID != "S2" || ix.ToolName != "" {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexResult(t *testing.T) {
	ix := DeriveIndex([]byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":3170,"total_cost_usd":0.0472883,"usage":{"input_tokens":10,"cache_creation_input_tokens":200,"cache_read_input_tokens":30,"output_tokens":6}}`))
	if ix.Subtype != "success" || ix.DurationMS != 3170 || ix.CostUSD != 0.0472883 || ix.InputTokens != 240 || ix.OutputTokens != 6 {
		t.Fatalf("%+v", ix)
	}
}

func TestDeriveIndexGarbage(t *testing.T) {
	if ix := DeriveIndex([]byte(`not json`)); ix != (Index{}) {
		t.Fatalf("%+v", ix)
	}
}
