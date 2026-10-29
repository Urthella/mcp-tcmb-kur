package main

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTCMB serves the real bulletins saved in testdata (26 and 27 Oct 2026) and 404s every
// other day, like TCMB does for weekends and holidays. 28-29 Oct 2026 were the Republic Day holiday.
func fakeTCMB(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	c.Now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, istanbul) }
	return c, &hits
}

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, istanbul) }

func TestParseRealBulletin(t *testing.T) {
	f, err := os.Open("testdata/27102026.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := parse(f)
	if err != nil {
		t.Fatal(err)
	}
	if b.Number != "2026/203" || !b.Date.Equal(day(27)) || len(b.Codes) != 22 || b.Codes[0] != "USD" {
		t.Fatalf("bulletin %s %v %d codes", b.Number, b.Date, len(b.Codes))
	}
	usd := b.Rates["USD"]
	if usd.Unit != 1 || num(usd.Quoted(ForexBuying), 4) != "49.6997" || usd.NameTR != "ABD DOLARI" {
		t.Errorf("USD: %+v %s", usd, num(usd.Quoted(ForexBuying), 4))
	}
	if b.Rates["JPY"].Unit != 100 {
		t.Errorf("JPY unit = %d", b.Rates["JPY"].Unit)
	}
	xdr := b.Rates["XDR"]
	if xdr.Quoted(ForexSelling) != nil || xdr.Quoted(ForexBuying) == nil || xdr.Name != "SPECIAL DRAWING RIGHT (SDR)" {
		t.Errorf("XDR blanks or padding not handled: %+v", xdr)
	}
}

func TestHolidayFallsBackToLastBulletin(t *testing.T) {
	c, hits := fakeTCMB(t)
	b, err := c.Latest(context.Background(), day(29))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Date.Equal(day(27)) || hits.Load() != 3 {
		t.Fatalf("got %v after %d requests; want 27 Oct after 3 (29, 28, 27)", b.Date, hits.Load())
	}
	// 28 and 27 are in the past and cached (including the 404); only today is asked again.
	if _, err := c.Latest(context.Background(), day(29)); err != nil || hits.Load() != 4 {
		t.Fatalf("cache: %d requests, err %v", hits.Load(), err)
	}
	if _, err := c.Latest(context.Background(), day(30)); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future date: %v", err)
	}
}

func TestConvert(t *testing.T) {
	c, _ := fakeTCMB(t)
	b, err := c.Latest(context.Background(), day(27))
	if err != nil {
		t.Fatal(err)
	}
	rat := func(s string) *big.Rat { r, _ := new(big.Rat).SetString(s); return r }
	cases := []struct {
		amount, from, to, kind, want string
	}{
		{"100", "USD", "TRY", ForexBuying, "4969.97"},  // 100 x 49.6997
		{"100", "usd", "TL", ForexSelling, "4978.92"},  // 100 x 49.7892, codes are case-insensitive
		{"1000", "JPY", "TRY", ForexBuying, "314.275"}, // per 100 yen: 1000 / 100 x 31.4275
		{"4969.97", "TRY", "USD", ForexBuying, "100"},  // and back
		{"0.1", "TRY", "TRY", BanknoteSelling, "0.1"},  // identity, exact decimal
	}
	for _, tc := range cases {
		got, err := b.Convert(rat(tc.amount), tc.from, tc.to, tc.kind)
		if err != nil || num(got, 4) != tc.want {
			t.Errorf("%s %s->%s %s = %s, %v; want %s", tc.amount, tc.from, tc.to, tc.kind, num(got, 4), err, tc.want)
		}
	}
	// Cross rate through the lira: EUR per USD is USD/TRY divided by EUR/TRY.
	usd, _ := b.TRYPer("USD", ForexBuying)
	eur, _ := b.TRYPer("EUR", ForexBuying)
	got, _ := b.Convert(rat("1"), "USD", "EUR", ForexBuying)
	if got.Cmp(new(big.Rat).Quo(usd, eur)) != 0 {
		t.Errorf("USD->EUR cross = %s", got.FloatString(6))
	}
	if _, err := b.Convert(rat("1"), "XDR", "TRY", ForexSelling); err == nil || !strings.Contains(err.Error(), "no forex_selling rate for XDR") {
		t.Errorf("blank rate: %v", err)
	}
	if _, err := b.Convert(rat("1"), "BTC", "TRY", ForexBuying); err == nil || !strings.Contains(err.Error(), "doesn't publish BTC") {
		t.Errorf("unknown code: %v", err)
	}
}

func TestNum(t *testing.T) {
	for in, want := range map[string]string{"49.69970": "49.6997", "100": "100", "314.275": "314.275", "0.00004": "0"} {
		r, _ := new(big.Rat).SetString(in)
		if got := num(r, 4); got != want {
			t.Errorf("num(%s) = %s, want %s", in, got, want)
		}
	}
	if num(nil, 4) != "-" {
		t.Error("nil rate should print -")
	}
}

// TestLiveTCMB hits the real site; run it with TCMB_LIVE=1 when checking the format still matches.
func TestLiveTCMB(t *testing.T) {
	if os.Getenv("TCMB_LIVE") == "" {
		t.Skip("set TCMB_LIVE=1 to call www.tcmb.gov.tr")
	}
	c := NewClient()
	b, err := c.Latest(context.Background(), c.Today())
	if err != nil {
		t.Fatal(err)
	}
	usd, err := b.TRYPer("USD", ForexBuying)
	if err != nil || usd.Sign() <= 0 || len(b.Codes) < 20 {
		t.Fatalf("bulletin %s: USD %v, %d codes, err %v", b.Number, usd, len(b.Codes), err)
	}
	t.Logf("bulletin %s of %s: 1 USD = %s TRY", b.Number, b.Date.Format("02.01.2006"), num(usd, 4))
}
