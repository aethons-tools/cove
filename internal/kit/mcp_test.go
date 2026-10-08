package kit

import (
	"strings"
	"testing"
)

func TestValidateMCPServersAccepts(t *testing.T) {
	servers := map[string]MCPServer{
		"linear": {Type: "http", URL: "${LINEAR_MCP_URL}", Headers: map[string]string{"Authorization": "Bearer ${LINEAR_TOKEN}"}},
		"plain":  {Type: "http", URL: "https://mcp.example.com/mcp", Headers: map[string]string{"X-Api-Key": "${EXAMPLE_KEY}"}},
		"local":  {Type: "stdio", Command: "my-mcp", Args: []string{"serve", "--root", "${HOME}"}},
	}
	if err := ValidateMCPServers("mcp-servers", servers); err != nil {
		t.Fatalf("valid servers rejected: %v", err)
	}
	if err := ValidateMCPServers("mcp-servers", nil); err != nil {
		t.Fatalf("no servers: %v", err)
	}
}

func TestValidateMCPServersRejects(t *testing.T) {
	cases := map[string]struct {
		servers map[string]MCPServer
		want    string
	}{
		"reserved messaging": {map[string]MCPServer{"messaging": {Type: "stdio", Command: "x"}}, "reserved"},
		"empty name":         {map[string]MCPServer{"": {Type: "stdio", Command: "x"}}, "name"},
		"bad name":           {map[string]MCPServer{"a b": {Type: "stdio", Command: "x"}}, "name"},
		"no type":            {map[string]MCPServer{"s": {Command: "x"}}, "type"},
		"sse type":           {map[string]MCPServer{"s": {Type: "sse", URL: "https://x"}}, "type"},
		"http no url":        {map[string]MCPServer{"s": {Type: "http"}}, "url"},
		"http with command":  {map[string]MCPServer{"s": {Type: "http", URL: "https://x", Command: "c"}}, "command"},
		"stdio no command":   {map[string]MCPServer{"s": {Type: "stdio"}}, "command"},
		"stdio with url":     {map[string]MCPServer{"s": {Type: "stdio", Command: "c", URL: "https://x"}}, "url"},
		"stdio with headers": {map[string]MCPServer{"s": {Type: "stdio", Command: "c", Headers: map[string]string{"A": "${B}"}}}, "headers"},
		"literal authorization": {map[string]MCPServer{"s": {Type: "http", URL: "https://x",
			Headers: map[string]string{"Authorization": "Bearer lin_api_abc123"}}}, "${VAR}"},
		"literal bare value": {map[string]MCPServer{"s": {Type: "http", URL: "https://x",
			Headers: map[string]string{"X-Api-Key": "abc123"}}}, "${VAR}"},
		"ref plus suffix": {map[string]MCPServer{"s": {Type: "http", URL: "https://x",
			Headers: map[string]string{"Authorization": "Bearer ${T}abc"}}}, "${VAR}"},
		"default syntax": {map[string]MCPServer{"s": {Type: "http", URL: "https://x",
			Headers: map[string]string{"Authorization": "${T:-secret}"}}}, "${VAR}"},
		"two-word prefix": {map[string]MCPServer{"s": {Type: "http", URL: "https://x",
			Headers: map[string]string{"Authorization": "Bearer abc ${T}"}}}, "${VAR}"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateMCPServers("mcp-servers", c.servers)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

// A rejected literal header value must never be echoed: it is likely a secret.
func TestValidateMCPServersNeverEchoesHeaderValue(t *testing.T) {
	const secret = "lin_api_SUPERSECRET"
	err := ValidateMCPServers("mcp-servers", map[string]MCPServer{
		"s": {Type: "http", URL: "https://x", Headers: map[string]string{"Authorization": "Bearer " + secret}},
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("want an error that does not echo the value, got %v", err)
	}
}
