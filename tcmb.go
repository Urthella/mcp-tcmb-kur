package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Turkey has used UTC+3 all year since 2016, so a fixed zone is exact and needs no tzdata.
var istanbul = time.FixedZone("TRT", 3*60*60)

// lookback covers the longest stretch without a bulletin: a nine-day Kurban Bayramı plus a weekend.
const lookback = 14

// Rate kinds as TCMB names them: forex rates for transfers, banknote ("efektif") rates for cash.
const (
	ForexBuying     = "forex_buying"
	ForexSelling    = "forex_selling"
	BanknoteBuying  = "banknote_buying"
	BanknoteSelling = "banknote_selling"
)

var Kinds = []string{ForexBuying, ForexSelling, BanknoteBuying, BanknoteSelling}

type Rate struct {
	Code, Name, NameTR string
	Unit               int64               // rates are quoted per Unit (100 for JPY)
	values             map[string]*big.Rat // kind -> TRY per Unit; missing when TCMB leaves it blank
}

type Bulletin struct {
	Date   time.Time
	Number string
	Codes  []string // in TCMB's order
	Rates  map[string]Rate
}

// Client fetches TCMB's daily indicative rate bulletins (www.tcmb.gov.tr/kurlar).
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time

	mu    sync.Mutex
	cache map[string]*Bulletin // past days only; nil means "no bulletin that day"
}

func NewClient() *Client {
	return &Client{BaseURL: "https://www.tcmb.gov.tr/kurlar", HTTP: &http.Client{Timeout: 15 * time.Second}, Now: time.Now}
}

// Today is the current date in Turkey.
func (c *Client) Today() time.Time {
	y, m, d := c.Now().In(istanbul).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, istanbul)
}

// Latest returns the bulletin for day, or for the last business day before it when TCMB
// published nothing that day (weekends, holidays, or today before the 15:30 publication).
func (c *Client) Latest(ctx context.Context, day time.Time) (*Bulletin, error) {
	if day.After(c.Today()) {
		return nil, fmt.Errorf("%s is in the future", day.Format("2006-01-02"))
	}
	for i := 0; i < lookback; i++ {
		b, err := c.Day(ctx, day.AddDate(0, 0, -i))
		if err != nil || b != nil {
			return b, err
		}
	}
	return nil, fmt.Errorf("no TCMB bulletin in the %d days up to %s", lookback, day.Format("2006-01-02"))
}

// Day returns the bulletin published on exactly that day, or nil if there was none.
func (c *Client) Day(ctx context.Context, day time.Time) (*Bulletin, error) {
	key := day.Format("2006-01-02")
	past := day.Before(c.Today()) // today's bulletin may still appear, so only past days are cached
	if past {
		c.mu.Lock()
		b, ok := c.cache[key]
		c.mu.Unlock()
		if ok {
			return b, nil
		}
	}

	url := fmt.Sprintf("%s/%s/%s.xml", c.BaseURL, day.Format("200601"), day.Format("02012006"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mcp-tcmb-kur/"+version+" (+https://github.com/Urthella/mcp-tcmb-kur)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TCMB: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var b *Bulletin
	switch resp.StatusCode {
	case http.StatusOK:
		if b, err = parse(io.LimitReader(resp.Body, 1<<20)); err != nil {
			return nil, err
		}
	case http.StatusNotFound: // weekend, holiday or not published yet
	default:
		return nil, fmt.Errorf("TCMB: %s for %s", resp.Status, key)
	}
	if past {
		c.mu.Lock()
		if c.cache == nil {
			c.cache = map[string]*Bulletin{}
		}
		c.cache[key] = b
		c.mu.Unlock()
	}
	return b, nil
}

func parse(r io.Reader) (*Bulletin, error) {
	var doc struct {
		Date       string `xml:"Tarih,attr"`
		Number     string `xml:"Bulten_No,attr"`
		Currencies []struct {
			Code            string `xml:"CurrencyCode,attr"`
			Unit            int64  `xml:"Unit"`
			NameTR          string `xml:"Isim"`
			Name            string `xml:"CurrencyName"`
			ForexBuying     string `xml:"ForexBuying"`
			ForexSelling    string `xml:"ForexSelling"`
			BanknoteBuying  string `xml:"BanknoteBuying"`
			BanknoteSelling string `xml:"BanknoteSelling"`
		} `xml:"Currency"`
	}
	if err := xml.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("TCMB: unexpected XML: %w", err)
	}
	date, err := time.ParseInLocation("02.01.2006", doc.Date, istanbul)
	if err != nil {
		return nil, fmt.Errorf("TCMB: bulletin date %q: %w", doc.Date, err)
	}
	b := &Bulletin{Date: date, Number: doc.Number, Rates: map[string]Rate{}}
	for _, c := range doc.Currencies {
		r := Rate{
			Code: c.Code, Unit: max(c.Unit, 1),
			Name: strings.TrimSpace(c.Name), NameTR: strings.TrimSpace(c.NameTR),
			values: map[string]*big.Rat{},
		}
		for kind, s := range map[string]string{
			ForexBuying: c.ForexBuying, ForexSelling: c.ForexSelling,
			BanknoteBuying: c.BanknoteBuying, BanknoteSelling: c.BanknoteSelling,
		} {
			if v, ok := new(big.Rat).SetString(strings.TrimSpace(s)); ok && v.Sign() > 0 {
				r.values[kind] = v
			}
		}
		b.Codes = append(b.Codes, r.Code)
		b.Rates[r.Code] = r
	}
	if len(b.Codes) == 0 {
		return nil, errors.New("TCMB: bulletin has no currencies")
	}
	return b, nil
}

// Quoted is the published figure (TRY per Unit), or nil if TCMB left it blank.
func (r Rate) Quoted(kind string) *big.Rat { return r.values[kind] }

// TRYPer returns how many lira one unit of code is worth. TRY itself is 1.
func (b *Bulletin) TRYPer(code, kind string) (*big.Rat, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "TRY" || code == "TL" {
		return big.NewRat(1, 1), nil
	}
	r, ok := b.Rates[code]
	if !ok {
		return nil, fmt.Errorf("TCMB doesn't publish %s; available: TRY, %s", code, strings.Join(b.Codes, ", "))
	}
	q := r.Quoted(kind)
	if q == nil {
		return nil, fmt.Errorf("TCMB publishes no %s rate for %s", kind, code)
	}
	return new(big.Rat).Quo(q, big.NewRat(r.Unit, 1)), nil
}

// Convert turns amount of from into to through lira, with exact decimal arithmetic.
func (b *Bulletin) Convert(amount *big.Rat, from, to, kind string) (*big.Rat, error) {
	f, err := b.TRYPer(from, kind)
	if err != nil {
		return nil, err
	}
	t, err := b.TRYPer(to, kind)
	if err != nil {
		return nil, err
	}
	out := new(big.Rat).Mul(amount, f)
	return out.Quo(out, t), nil
}

const maxHistoryDays = 62

type Point struct {
	Date time.Time
	Rate *big.Rat // TRY per Unit, as published
}

// History returns one point per bulletin between from and to, skipping days without one.
func (c *Client) History(ctx context.Context, code, kind string, from, to time.Time) ([]Point, int64, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if today := c.Today(); to.After(today) {
		to = today
	}
	if to.Before(from) {
		return nil, 0, errors.New("from must not be after to")
	}
	if days := int(to.Sub(from).Hours()/24) + 1; days > maxHistoryDays {
		return nil, 0, fmt.Errorf("at most %d days per call, asked for %d", maxHistoryDays, days)
	}
	var points []Point
	var unit int64
	// ponytail: one request per day, sequential and cached; plenty for two months of history.
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		b, err := c.Day(ctx, d)
		if err != nil {
			return nil, 0, err
		}
		if b == nil {
			continue
		}
		r, ok := b.Rates[code]
		if !ok {
			return nil, 0, fmt.Errorf("TCMB doesn't publish %s; available: %s", code, strings.Join(b.Codes, ", "))
		}
		q := r.Quoted(kind)
		if q == nil {
			return nil, 0, fmt.Errorf("TCMB publishes no %s rate for %s", kind, code)
		}
		points = append(points, Point{Date: b.Date, Rate: q})
		unit = r.Unit
	}
	if len(points) == 0 {
		return nil, 0, fmt.Errorf("no TCMB bulletin between %s and %s", from.Format("2006-01-02"), to.Format("2006-01-02"))
	}
	return points, unit, nil
}

func validKind(kind string) (string, error) {
	if kind == "" {
		return ForexBuying, nil
	}
	for _, k := range Kinds {
		if kind == k {
			return k, nil
		}
	}
	return "", fmt.Errorf("rate must be one of %s", strings.Join(Kinds, ", "))
}
