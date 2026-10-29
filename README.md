# mcp-tcmb-kur

[![ci](https://github.com/Urthella/mcp-tcmb-kur/actions/workflows/ci.yml/badge.svg)](https://github.com/Urthella/mcp-tcmb-kur/actions/workflows/ci.yml)

**TR** · Türkiye Cumhuriyet Merkez Bankası'nın (TCMB) gösterge niteliğindeki günlük döviz kurları için bir [MCP](https://modelcontextprotocol.io) sunucusu. Claude Code, Cursor ve diğer MCP istemcileri "dün dolar kaç TL'ydi?" ya da "2.500 EUR efektif satıştan kaç TL?" gibi soruları gerçek TCMB bülteninden cevaplar. API anahtarı gerekmez.

**EN** · An MCP server for the Central Bank of the Republic of Türkiye's (TCMB) daily indicative exchange rates, so coding agents and chat clients answer currency questions from the official bulletin. No API key needed.

```bash
go install github.com/Urthella/mcp-tcmb-kur@latest
claude mcp add tcmb-kur -- mcp-tcmb-kur
```

## Örnek / Example

29 Ekim 2026 Cumhuriyet Bayramı'nda sorulan "100 dolar kaç TL?" sorusunun cevabı / asked on a public holiday:

```text
100 USD = 4969.97 TRY

At forex_buying (1 USD = 49.6997 TRY). TCMB bulletin 2026/203 of 27.10.2026. No bulletin was published on 29.10.2026 (weekend, holiday, or before the 15:30 release), so this is the previous one.
```

## Araçlar / Tools

| Tool | |
|---|---|
| `rates` | A day's bulletin: forex and banknote (efektif) buying/selling for all 22 currencies, or only the ones asked for |
| `convert` | Any amount between any two TCMB currencies or TRY, at the chosen rate; cross rates go through the lira |
| `history` | One currency's daily rate over up to 62 days, with day-over-day change, overall change, low and high |

Rate kinds: `forex_buying` (döviz alış, default), `forex_selling` (döviz satış), `banknote_buying` (efektif alış), `banknote_selling` (efektif satış).

## Doğruluk / Correctness

- **Tatiller ve hafta sonu / Holidays and weekends**: TCMB publishes on business days around 15:30. When there's no bulletin for the requested day, the server walks back up to 14 days (long enough for a nine-day Kurban Bayramı) and says which bulletin it used.
- **Birim / Units**: some currencies are quoted per 100 (JPY). Conversions divide by the published unit, so 1000 JPY isn't off by 100x.
- **Tam ondalık / Exact decimals**: amounts and rates are `math/big.Rat`, so there's no floating-point drift in money math.
- **Boş kurlar / Blank rates**: TCMB publishes only a forex buying rate for XDR (SDR); asking for another kind is a clear error, not a zero.
- **Önbellek / Cache**: past days never change, so they are cached in memory, including days with no bulletin.

Tests run on two real TCMB bulletins (26 and 27 Oct 2026) served by a fake server. `TCMB_LIVE=1 go test ./...` also checks the live site still matches the format.

> Kurlar gösterge niteliğindedir; resmi işlemlerde TCMB'nin yayımladığı bülteni esas alın. / Rates are indicative; check TCMB's own bulletin for anything official.

## Develop

```bash
nix develop     # Go, gopls, golangci-lint
go test ./...
```

## License

[MIT](LICENSE)
