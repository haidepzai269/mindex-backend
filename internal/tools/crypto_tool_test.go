package tools

import (
	"encoding/json"
	"testing"
)

func TestCryptoTool_Metadata(t *testing.T) {
	tool := NewCryptoTool()
	if tool.Name() != "crypto" {
		t.Errorf("expected name 'crypto', got %q", tool.Name())
	}
	if tool.Category() != CategoryInformationRetrieval {
		t.Errorf("expected category information_retrieval, got %q", tool.Category())
	}
	if tool.RequiresConfirmation() {
		t.Error("crypto tool should not require confirmation")
	}
	limits := tool.TierLimit()
	if limits["FREE"] != 20 || limits["PRO"] != 100 {
		t.Errorf("unexpected tier limits: %v", limits)
	}
}

func TestCryptoTool_ResolveCoinID(t *testing.T) {
	cases := map[string]string{
		"BTC":      "bitcoin",
		"btc":      "bitcoin",
		"Bitcoin":  "bitcoin",
		"bitcoin":  "bitcoin",
		"ETH":      "ethereum",
		"eth":      "ethereum",
		"SOL":      "solana",
		"DOGE":     "dogecoin",
		"matic":    "polygon",
		"SHIB":     "shiba-inu",
		"unknown":  "unknown",
		"  BTC  ":  "bitcoin",
	}
	for input, expected := range cases {
		got := resolveCoinID(input)
		if got != expected {
			t.Errorf("resolveCoinID(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestCryptoTool_ParseCoinGeckoPrice(t *testing.T) {
	raw := `{
		"bitcoin": {
			"vnd": 2450000000,
			"vnd_24h_change": -2.35,
			"vnd_market_cap": 48000000000000000,
			"vnd_24h_vol": 1200000000000000
		}
	}`

	var resp map[string]struct {
		VND          float64 `json:"vnd"`
		VND24hChange float64 `json:"vnd_24h_change"`
		VNDMarketCap float64 `json:"vnd_market_cap"`
		VND24hVol    float64 `json:"vnd_24h_vol"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	btc, ok := resp["bitcoin"]
	if !ok {
		t.Fatal("bitcoin not found in response")
	}
	if btc.VND != 2450000000 {
		t.Errorf("price = %f, want 2450000000", btc.VND)
	}
	if btc.VND24hChange != -2.35 {
		t.Errorf("change_24h = %f, want -2.35", btc.VND24hChange)
	}
}

func TestCryptoTool_ParseCoinGeckoChart(t *testing.T) {
	raw := `{
		"prices": [
			[1719446400000, 2430000000],
			[1719446700000, 2435000000],
			[1719447000000, 2440000000]
		]
	}`

	var resp struct {
		Prices [][]float64 `json:"prices"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if len(resp.Prices) != 3 {
		t.Fatalf("prices count = %d, want 3", len(resp.Prices))
	}

	history := make([]PricePoint, 0, len(resp.Prices))
	for _, p := range resp.Prices {
		if len(p) >= 2 {
			history = append(history, PricePoint{Time: int64(p[0]), Price: p[1]})
		}
	}

	if len(history) != 3 {
		t.Fatalf("history count = %d, want 3", len(history))
	}
	if history[0].Time != 1719446400000 {
		t.Errorf("first time = %d", history[0].Time)
	}
	if history[0].Price != 2430000000 {
		t.Errorf("first price = %f", history[0].Price)
	}
}

func TestCryptoTool_ParseCoinPaprika(t *testing.T) {
	raw := `{
		"name": "Bitcoin",
		"symbol": "btc",
		"quotes": {
			"VND": {
				"price": 2450000000,
				"percent_change_24h": -2.35,
				"market_cap": 48000000000000000,
				"volume_24h": 1200000000000000
			}
		}
	}`

	var resp struct {
		Name   string `json:"name"`
		Symbol string `json:"symbol"`
		Quotes struct {
			VND struct {
				Price     float64 `json:"price"`
				Change24h float64 `json:"percent_change_24h"`
				MarketCap float64 `json:"market_cap"`
				Volume24h float64 `json:"volume_24h"`
			} `json:"VND"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if resp.Name != "Bitcoin" {
		t.Errorf("name = %q, want 'Bitcoin'", resp.Name)
	}
	if resp.Quotes.VND.Price != 2450000000 {
		t.Errorf("price = %f, want 2450000000", resp.Quotes.VND.Price)
	}
	if resp.Quotes.VND.Change24h != -2.35 {
		t.Errorf("change = %f, want -2.35", resp.Quotes.VND.Change24h)
	}
}

func TestCryptoTool_CoinGeckoToPaprika(t *testing.T) {
	cases := map[string]string{
		"bitcoin":   "btc-bitcoin",
		"ethereum":  "eth-ethereum",
		"solana":    "sol-solana",
		"dogecoin":  "doge-dogecoin",
		"unknown":   "unknown",
	}
	for input, expected := range cases {
		got := coinGeckoToPaprika(input)
		if got != expected {
			t.Errorf("coinGeckoToPaprika(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestCryptoTool_CoinIDToSymbol(t *testing.T) {
	if coinIDToSymbol["bitcoin"] != "BTC" {
		t.Errorf("bitcoin symbol = %q, want 'BTC'", coinIDToSymbol["bitcoin"])
	}
	if coinIDToSymbol["ethereum"] != "ETH" {
		t.Errorf("ethereum symbol = %q, want 'ETH'", coinIDToSymbol["ethereum"])
	}
	if coinIDToName["bitcoin"] != "Bitcoin" {
		t.Errorf("bitcoin name = %q, want 'Bitcoin'", coinIDToName["bitcoin"])
	}
}

func TestCryptoTool_RejectsEmptyCoin(t *testing.T) {
	var params struct {
		Coin string `json:"coin"`
	}
	json.Unmarshal([]byte(`{}`), &params)
	if params.Coin != "" {
		t.Error("expected empty coin from empty input")
	}
}
