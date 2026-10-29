package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func connect(t *testing.T, c *Client) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := newServer(c).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func TestMCPTools(t *testing.T) {
	c, _ := fakeTCMB(t)
	cs := connect(t, c)

	out, isErr := call(t, cs, "rates", map[string]any{"currencies": []string{"usd", "JPY"}})
	for _, want := range []string{
		"TCMB bulletin 2026/203 of 27.10.2026. No bulletin was published on 29.10.2026",
		"| USD | 1 | 49.6997 | 49.7892 |",
		"| JPY | 100 | 31.4275 |",
	} {
		if isErr || !strings.Contains(out, want) {
			t.Errorf("rates missing %q (isError=%v):\n%s", want, isErr, out)
		}
	}
	if strings.Contains(out, "| EUR |") {
		t.Error("rates ignored the currency filter")
	}

	out, isErr = call(t, cs, "convert", map[string]any{"amount": 250, "from": "EUR", "to": "TRY", "date": "2026-10-26", "rate": "banknote_selling"})
	if isErr || !strings.HasPrefix(out, "250 EUR = ") || !strings.Contains(out, "bulletin 2026/202 of 26.10.2026") || strings.Contains(out, "No bulletin") {
		t.Errorf("convert (isError=%v):\n%s", isErr, out)
	}

	if out, isErr = call(t, cs, "convert", map[string]any{"amount": 1, "from": "USD", "to": "TRY", "rate": "midpoint"}); !isErr || !strings.Contains(out, "rate must be one of") {
		t.Errorf("bad rate kind accepted: %s", out)
	}
	if out, isErr = call(t, cs, "rates", map[string]any{"date": "29/10/2026"}); !isErr || !strings.Contains(out, "YYYY-MM-DD") {
		t.Errorf("bad date accepted: %s", out)
	}
}
