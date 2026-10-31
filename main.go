// Command mcp-tcmb-kur is an MCP server for the Turkish central bank's (TCMB) daily indicative
// exchange rates: look up a day's bulletin and convert between currencies.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const version = "0.1.0"

const rateDoc = "forex_buying (default), forex_selling, banknote_buying or banknote_selling (banknote = efektif, for cash)"

type ratesInput struct {
	Date       string   `json:"date,omitempty" jsonschema:"day as YYYY-MM-DD, default today; weekends and holidays fall back to the previous bulletin"`
	Currencies []string `json:"currencies,omitempty" jsonschema:"ISO codes like USD or EUR; default all 22 TCMB currencies"`
}

type convertInput struct {
	Amount float64 `json:"amount" jsonschema:"amount to convert"`
	From   string  `json:"from" jsonschema:"currency code, TRY included"`
	To     string  `json:"to" jsonschema:"currency code, TRY included"`
	Date   string  `json:"date,omitempty" jsonschema:"day as YYYY-MM-DD, default today"`
	Rate   string  `json:"rate,omitempty" jsonschema:"one of: forex_buying (default), forex_selling, banknote_buying, banknote_selling"`
}

type historyInput struct {
	Currency string `json:"currency" jsonschema:"currency code like USD"`
	From     string `json:"from,omitempty" jsonschema:"first day as YYYY-MM-DD, default 30 days before to"`
	To       string `json:"to,omitempty" jsonschema:"last day as YYYY-MM-DD, default today"`
	Rate     string `json:"rate,omitempty" jsonschema:"one of: forex_buying (default), forex_selling, banknote_buying, banknote_selling"`
}

func newServer(c *Client) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-tcmb-kur", Version: version}, nil)
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "rates",
		Description: "TCMB indicative exchange rates against the Turkish lira for a day: forex and banknote buying/selling, per the published unit.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ratesInput) (*mcp.CallToolResult, any, error) {
		b, note, err := bulletinFor(ctx, c, in.Date)
		if err != nil {
			return nil, nil, err
		}
		codes := b.Codes
		if len(in.Currencies) > 0 {
			codes = nil
			for _, code := range in.Currencies {
				code = strings.ToUpper(strings.TrimSpace(code))
				if _, ok := b.Rates[code]; !ok {
					return nil, nil, fmt.Errorf("TCMB doesn't publish %s; available: %s", code, strings.Join(b.Codes, ", "))
				}
				codes = append(codes, code)
			}
		}
		var t strings.Builder
		t.WriteString(note + "\n\n| Code | Unit | Forex buying | Forex selling | Banknote buying | Banknote selling | Name |\n|---|---|---|---|---|---|---|\n")
		for _, code := range codes {
			r := b.Rates[code]
			fmt.Fprintf(&t, "| %s | %d |", code, r.Unit)
			for _, k := range Kinds {
				fmt.Fprintf(&t, " %s |", num(r.Quoted(k), 4))
			}
			fmt.Fprintf(&t, " %s / %s |\n", r.Name, r.NameTR)
		}
		t.WriteString("\nRates are Turkish lira per Unit of the currency.")
		return text(t.String()), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "convert",
		Description: "Convert an amount between any two TCMB currencies or TRY, at a TCMB rate (" + rateDoc + ") for a day. Cross rates go through the lira.",
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in convertInput) (*mcp.CallToolResult, any, error) {
		kind, err := validKind(in.Rate)
		if err != nil {
			return nil, nil, err
		}
		if in.Amount <= 0 {
			return nil, nil, errors.New("amount must be positive")
		}
		// The shortest decimal that round-trips, so 0.1 stays exactly 0.1 instead of a binary float.
		amount, _ := new(big.Rat).SetString(strconv.FormatFloat(in.Amount, 'f', -1, 64))
		b, note, err := bulletinFor(ctx, c, in.Date)
		if err != nil {
			return nil, nil, err
		}
		out, err := b.Convert(amount, in.From, in.To, kind)
		if err != nil {
			return nil, nil, err
		}
		from, to := strings.ToUpper(in.From), strings.ToUpper(in.To)
		var legs []string
		for _, code := range []string{from, to} {
			if code != "TRY" && code != "TL" {
				r := b.Rates[code]
				legs = append(legs, fmt.Sprintf("%d %s = %s TRY", r.Unit, code, num(r.Quoted(kind), 4)))
			}
		}
		return text(fmt.Sprintf("%s %s = %s %s\n\nAt %s (%s). %s",
			num(amount, 4), from, num(out, 4), to, kind, strings.Join(legs, ", "), note)), nil, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "history",
		Description: fmt.Sprintf("Daily TCMB rate of one currency over up to %d days, with change, low and high. Days without a bulletin are skipped.", maxHistoryDays),
		Annotations: ro,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyInput) (*mcp.CallToolResult, any, error) {
		kind, err := validKind(in.Rate)
		if err != nil {
			return nil, nil, err
		}
		to, err := parseDay(in.To, c.Today())
		if err != nil {
			return nil, nil, err
		}
		from, err := parseDay(in.From, to.AddDate(0, 0, -30))
		if err != nil {
			return nil, nil, err
		}
		points, unit, err := c.History(ctx, in.Currency, kind, from, to)
		if err != nil {
			return nil, nil, err
		}
		code := strings.ToUpper(in.Currency)
		var t strings.Builder
		fmt.Fprintf(&t, "%s %s in TRY per %d %s, %d bulletins\n\n| Date | Rate | Change |\n|---|---|---|\n", code, kind, unit, code, len(points))
		low, high := points[0], points[0]
		for i, p := range points {
			change := ""
			if i > 0 {
				change = pct(points[i-1].Rate, p.Rate)
			}
			fmt.Fprintf(&t, "| %s | %s | %s |\n", p.Date.Format("02.01.2006"), num(p.Rate, 4), change)
			if p.Rate.Cmp(low.Rate) < 0 {
				low = p
			}
			if p.Rate.Cmp(high.Rate) > 0 {
				high = p
			}
		}
		first, last := points[0], points[len(points)-1]
		fmt.Fprintf(&t, "\nFirst %s, last %s: %s. Low %s on %s, high %s on %s.",
			num(first.Rate, 4), num(last.Rate, 4), pct(first.Rate, last.Rate),
			num(low.Rate, 4), low.Date.Format("02.01.2006"), num(high.Rate, 4), high.Date.Format("02.01.2006"))
		return text(t.String()), nil, nil
	})

	return s
}

func parseDay(s string, def time.Time) (time.Time, error) {
	if s == "" {
		return def, nil
	}
	d, err := time.ParseInLocation("2006-01-02", s, istanbul)
	if err != nil {
		return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD, got %q", s)
	}
	return d, nil
}

// pct is the signed percentage change from a to b, to two decimals.
func pct(a, b *big.Rat) string {
	p := new(big.Rat).Sub(b, a)
	p.Quo(p, a).Mul(p, big.NewRat(100, 1))
	s := p.FloatString(2) + "%"
	if p.Sign() >= 0 {
		s = "+" + s
	}
	return s
}

// bulletinFor resolves "" to today and explains any fallback to an earlier bulletin.
func bulletinFor(ctx context.Context, c *Client, date string) (*Bulletin, string, error) {
	day := c.Today()
	if date != "" {
		d, err := time.ParseInLocation("2006-01-02", date, istanbul)
		if err != nil {
			return nil, "", fmt.Errorf("date must be YYYY-MM-DD, got %q", date)
		}
		day = d
	}
	b, err := c.Latest(ctx, day)
	if err != nil {
		return nil, "", err
	}
	note := fmt.Sprintf("TCMB bulletin %s of %s.", b.Number, b.Date.Format("02.01.2006"))
	if !b.Date.Equal(day) {
		note += fmt.Sprintf(" No bulletin was published on %s (weekend, holiday, or before the 15:30 release), so this is the previous one.", day.Format("02.01.2006"))
	}
	return b, note, nil
}

// num prints a rate or amount with at most places decimals, without trailing zeros; "-" when missing.
func num(r *big.Rat, places int) string {
	if r == nil {
		return "-"
	}
	s := r.FloatString(places)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func main() {
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := newServer(NewClient()).Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp-tcmb-kur: %v", err)
	}
}
