package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mindex-backend/config"
)

const (
	cryptoPriceCacheTTL    = 60 * time.Second
	cryptoChartCacheTTL    = 5 * time.Minute
	cryptoProviderTimeout  = 2 * time.Second
	exchangeRateCacheTTL   = 1 * time.Hour
	defaultUSDVNDRate      = 25500.0
)

type CryptoPrice struct {
	Name         string       `json:"name"`
	Symbol       string       `json:"symbol"`
	PriceVND     float64      `json:"price_vnd"`
	Change24h    float64      `json:"change_24h"`
	MarketCapVND float64      `json:"market_cap_vnd"`
	Volume24hVND float64      `json:"volume_24h_vnd"`
	PriceHistory []PricePoint `json:"price_history"`
}

type PricePoint struct {
	Time  int64   `json:"time"`
	Price float64 `json:"price"`
}

var commonCoinAliases = map[string]string{
	"btc": "bitcoin", "eth": "ethereum", "bnb": "binancecoin",
	"sol": "solana", "xrp": "ripple", "ada": "cardano",
	"doge": "dogecoin", "dot": "polkadot", "matic": "polygon",
	"avax": "avalanche-2", "link": "chainlink", "uni": "uniswap",
	"shib": "shiba-inu", "ltc": "litecoin", "atom": "cosmos",
	"near": "near", "trx": "tron", "sui": "sui",
	"bitcoin": "bitcoin", "ethereum": "ethereum", "solana": "solana",
	"dogecoin": "dogecoin", "cardano": "cardano", "ripple": "ripple",
	"polkadot": "polkadot", "chainlink": "chainlink", "litecoin": "litecoin",
	"tron": "tron", "uniswap": "uniswap",
}

type cryptoTool struct{}

func NewCryptoTool() Tool { return &cryptoTool{} }

func (t *cryptoTool) Name() string        { return "crypto" }
func (t *cryptoTool) Description() string  { return "Tra cứu giá tiền điện tử (crypto) theo VND, biến động 24h và biểu đồ giá" }
func (t *cryptoTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *cryptoTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"coin": map[string]interface{}{
				"type":        "string",
				"description": "Tên hoặc symbol coin (ví dụ: bitcoin, BTC, ethereum, ETH)",
			},
		},
		"required": []string{"coin"},
	}
}
func (t *cryptoTool) Timeout() time.Duration        { return 5 * time.Second }
func (t *cryptoTool) TierLimit() map[string]int      { return map[string]int{"FREE": 20, "PRO": 100} }
func (t *cryptoTool) RequiresConfirmation() bool      { return false }

func (t *cryptoTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Coin string `json:"coin"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{}, fmt.Errorf("tham số không hợp lệ: %v", err)
	}
	coin := strings.TrimSpace(params.Coin)
	if coin == "" {
		return ToolResult{}, fmt.Errorf("thiếu tên coin")
	}
	if len(coin) > 100 {
		return ToolResult{}, fmt.Errorf("tên coin quá dài")
	}

	coinID := resolveCoinID(coin)

	queryHash := HashQuery("crypto", coinID)
	data, err := CachedExecute(ctx, "crypto", queryHash, CacheScopeGlobal, "", cryptoPriceCacheTTL, func() (json.RawMessage, error) {
		return fetchCrypto(ctx, coinID)
	})
	if err != nil {
		return ToolResult{}, err
	}

	richContent, err := json.Marshal(map[string]interface{}{
		"type":      "crypto",
		"data":      json.RawMessage(data),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		richContent = nil
	}

	return ToolResult{
		JSONOutput:  data,
		RichContent: richContent,
	}, nil
}

func resolveCoinID(input string) string {
	normalized := strings.ToLower(strings.TrimSpace(input))
	if id, ok := commonCoinAliases[normalized]; ok {
		return id
	}
	return normalized
}

func fetchCrypto(ctx context.Context, coinID string) (json.RawMessage, error) {
	if key := config.Env.CoinGeckoAPIKey; key != "" {
		pCtx, pCancel := context.WithTimeout(ctx, cryptoProviderTimeout)
		data, err := fetchCoinGecko(pCtx, coinID, key)
		pCancel()
		if err == nil {
			return data, nil
		}
	}

	pCtx, pCancel := context.WithTimeout(ctx, cryptoProviderTimeout)
	data, err := fetchCoinGeckoFree(pCtx, coinID)
	pCancel()
	if err == nil {
		return data, nil
	}

	pCtx2, pCancel2 := context.WithTimeout(ctx, cryptoProviderTimeout)
	data, err = fetchCoinPaprika(pCtx2, coinID)
	pCancel2()
	if err == nil {
		return data, nil
	}

	pCtx3, pCancel3 := context.WithTimeout(ctx, cryptoProviderTimeout)
	data, err = fetchBinance(pCtx3, coinID)
	pCancel3()
	if err == nil {
		return data, nil
	}

	pCtx4, pCancel4 := context.WithTimeout(ctx, cryptoProviderTimeout)
	data, err = fetchOKX(pCtx4, coinID)
	pCancel4()
	if err == nil {
		return data, nil
	}

	return nil, fmt.Errorf("không có provider crypto nào khả dụng")
}

func fetchCoinGecko(ctx context.Context, coinID, apiKey string) (json.RawMessage, error) {
	priceURL := fmt.Sprintf(
		"https://pro-api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=vnd&include_24hr_change=true&include_market_cap=true&include_24hr_vol=true&x_cg_pro_api_key=%s",
		coinID, apiKey,
	)
	return fetchCoinGeckoData(ctx, coinID, priceURL, apiKey)
}

func fetchCoinGeckoFree(ctx context.Context, coinID string) (json.RawMessage, error) {
	priceURL := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=vnd&include_24hr_change=true&include_market_cap=true&include_24hr_vol=true",
		coinID,
	)
	return fetchCoinGeckoData(ctx, coinID, priceURL, "")
}

func fetchCoinGeckoData(ctx context.Context, coinID, priceURL, apiKey string) (json.RawMessage, error) {
	priceBody, err := httpGetJSON(ctx, priceURL)
	if err != nil {
		return nil, fmt.Errorf("CoinGecko price: %w", err)
	}

	var priceResp map[string]struct {
		VND            float64 `json:"vnd"`
		VND24hChange   float64 `json:"vnd_24h_change"`
		VNDMarketCap   float64 `json:"vnd_market_cap"`
		VND24hVol      float64 `json:"vnd_24h_vol"`
	}
	if err := json.Unmarshal(priceBody, &priceResp); err != nil {
		return nil, fmt.Errorf("CoinGecko price parse: %w", err)
	}
	coinData, ok := priceResp[coinID]
	if !ok {
		return nil, fmt.Errorf("coin '%s' không tìm thấy trên CoinGecko", coinID)
	}

	chartURL := fmt.Sprintf(
		"https://api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=vnd&days=1",
		coinID,
	)
	if apiKey != "" {
		chartURL = fmt.Sprintf(
			"https://pro-api.coingecko.com/api/v3/coins/%s/market_chart?vs_currency=vnd&days=1&x_cg_pro_api_key=%s",
			coinID, apiKey,
		)
	}

	var history []PricePoint
	chartBody, chartErr := httpGetJSON(ctx, chartURL)
	if chartErr == nil {
		var chartResp struct {
			Prices [][]float64 `json:"prices"`
		}
		if json.Unmarshal(chartBody, &chartResp) == nil {
			for _, p := range chartResp.Prices {
				if len(p) >= 2 {
					history = append(history, PricePoint{
						Time:  int64(p[0]),
						Price: p[1],
					})
				}
			}
		}
	}

	coinName := titleCase(strings.ReplaceAll(coinID, "-", " "))
	symbol := strings.ToUpper(coinID)
	if s, ok := coinIDToSymbol[coinID]; ok {
		symbol = s
		coinName = coinIDToName[coinID]
	}

	result := CryptoPrice{
		Name:         coinName,
		Symbol:       symbol,
		PriceVND:     coinData.VND,
		Change24h:    coinData.VND24hChange,
		MarketCapVND: coinData.VNDMarketCap,
		Volume24hVND: coinData.VND24hVol,
		PriceHistory: history,
	}
	return json.Marshal(result)
}

var coinIDToSymbol = map[string]string{
	"bitcoin": "BTC", "ethereum": "ETH", "binancecoin": "BNB",
	"solana": "SOL", "ripple": "XRP", "cardano": "ADA",
	"dogecoin": "DOGE", "polkadot": "DOT", "polygon": "MATIC",
	"avalanche-2": "AVAX", "chainlink": "LINK", "uniswap": "UNI",
	"shiba-inu": "SHIB", "litecoin": "LTC", "cosmos": "ATOM",
	"near": "NEAR", "tron": "TRX", "sui": "SUI",
}

var coinIDToName = map[string]string{
	"bitcoin": "Bitcoin", "ethereum": "Ethereum", "binancecoin": "BNB",
	"solana": "Solana", "ripple": "XRP", "cardano": "Cardano",
	"dogecoin": "Dogecoin", "polkadot": "Polkadot", "polygon": "Polygon",
	"avalanche-2": "Avalanche", "chainlink": "Chainlink", "uniswap": "Uniswap",
	"shiba-inu": "Shiba Inu", "litecoin": "Litecoin", "cosmos": "Cosmos",
	"near": "NEAR Protocol", "tron": "TRON", "sui": "Sui",
}

func fetchCoinPaprika(ctx context.Context, coinID string) (json.RawMessage, error) {
	paprikaID := coinGeckoToPaprika(coinID)

	tickerURL := fmt.Sprintf("https://api.coinpaprika.com/v1/tickers/%s?quotes=VND", paprikaID)
	body, err := httpGetJSON(ctx, tickerURL)
	if err != nil {
		return nil, fmt.Errorf("CoinPaprika: %w", err)
	}

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
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("CoinPaprika parse: %w", err)
	}

	result := CryptoPrice{
		Name:         resp.Name,
		Symbol:       strings.ToUpper(resp.Symbol),
		PriceVND:     resp.Quotes.VND.Price,
		Change24h:    resp.Quotes.VND.Change24h,
		MarketCapVND: resp.Quotes.VND.MarketCap,
		Volume24hVND: resp.Quotes.VND.Volume24h,
		PriceHistory: []PricePoint{},
	}
	return json.Marshal(result)
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func fetchBinance(ctx context.Context, coinID string) (json.RawMessage, error) {
	symbol, ok := coinIDToSymbol[coinID]
	if !ok {
		return nil, fmt.Errorf("Binance: coin '%s' không có symbol mapping", coinID)
	}

	reqURL := fmt.Sprintf("https://api.binance.com/api/v3/ticker/24hr?symbol=%sUSDT", symbol)
	body, err := httpGetJSON(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("Binance: %w", err)
	}

	var resp struct {
		LastPrice          string `json:"lastPrice"`
		PriceChangePercent string `json:"priceChangePercent"`
		QuoteVolume        string `json:"quoteVolume"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("Binance parse: %w", err)
	}

	rate := getUSDVNDRate(ctx)
	lastPrice := parseFloat(resp.LastPrice)
	change := parseFloat(resp.PriceChangePercent)
	quoteVol := parseFloat(resp.QuoteVolume)

	coinName := titleCase(strings.ReplaceAll(coinID, "-", " "))
	if n, ok := coinIDToName[coinID]; ok {
		coinName = n
	}

	result := CryptoPrice{
		Name:         coinName,
		Symbol:       symbol,
		PriceVND:     lastPrice * rate,
		Change24h:    change,
		MarketCapVND: 0,
		Volume24hVND: quoteVol * rate,
		PriceHistory: []PricePoint{},
	}
	return json.Marshal(result)
}

func fetchOKX(ctx context.Context, coinID string) (json.RawMessage, error) {
	symbol, ok := coinIDToSymbol[coinID]
	if !ok {
		return nil, fmt.Errorf("OKX: coin '%s' không có symbol mapping", coinID)
	}

	reqURL := fmt.Sprintf("https://www.okx.com/api/v5/market/ticker?instId=%s-USDT", symbol)
	body, err := httpGetJSON(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("OKX: %w", err)
	}

	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Last      string `json:"last"`
			Open24h   string `json:"open24h"`
			VolCcy24h string `json:"volCcy24h"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("OKX parse: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("OKX: pair %s-USDT not found", symbol)
	}

	d := resp.Data[0]
	rate := getUSDVNDRate(ctx)
	last := parseFloat(d.Last)
	open24h := parseFloat(d.Open24h)
	volCcy := parseFloat(d.VolCcy24h)

	var change24h float64
	if open24h > 0 {
		change24h = ((last - open24h) / open24h) * 100
	}

	coinName := titleCase(strings.ReplaceAll(coinID, "-", " "))
	if n, ok := coinIDToName[coinID]; ok {
		coinName = n
	}

	result := CryptoPrice{
		Name:         coinName,
		Symbol:       symbol,
		PriceVND:     last * rate,
		Change24h:    change24h,
		MarketCapVND: 0,
		Volume24hVND: volCcy * rate,
		PriceHistory: []PricePoint{},
	}
	return json.Marshal(result)
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

func getUSDVNDRate(ctx context.Context) float64 {
	queryHash := HashQuery("exchange_rate", "usd_vnd")
	data, err := CachedExecute(ctx, "exchange_rate", queryHash, CacheScopeGlobal, "", exchangeRateCacheTTL, func() (json.RawMessage, error) {
		rateCtx, cancel := context.WithTimeout(ctx, cryptoProviderTimeout)
		defer cancel()
		body, err := httpGetJSON(rateCtx, "https://open.er-api.com/v6/latest/USD")
		if err != nil {
			return json.Marshal(defaultUSDVNDRate)
		}
		var resp struct {
			Result string             `json:"result"`
			Rates  map[string]float64 `json:"rates"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || resp.Result != "success" {
			return json.Marshal(defaultUSDVNDRate)
		}
		vnd, ok := resp.Rates["VND"]
		if !ok || vnd <= 0 {
			return json.Marshal(defaultUSDVNDRate)
		}
		return json.Marshal(vnd)
	})
	if err != nil {
		return defaultUSDVNDRate
	}
	var rate float64
	if err := json.Unmarshal(data, &rate); err != nil || rate <= 0 {
		return defaultUSDVNDRate
	}
	return rate
}

func coinGeckoToPaprika(geckoID string) string {
	mapping := map[string]string{
		"bitcoin": "btc-bitcoin", "ethereum": "eth-ethereum",
		"binancecoin": "bnb-binance-coin", "solana": "sol-solana",
		"ripple": "xrp-xrp", "cardano": "ada-cardano",
		"dogecoin": "doge-dogecoin", "polkadot": "dot-polkadot",
		"polygon": "matic-polygon", "avalanche-2": "avax-avalanche",
		"chainlink": "link-chainlink", "uniswap": "uni-uniswap",
		"shiba-inu": "shib-shiba-inu", "litecoin": "ltc-litecoin",
		"cosmos": "atom-cosmos", "near": "near-near-protocol",
		"tron": "trx-tron", "sui": "sui-sui",
	}
	if id, ok := mapping[geckoID]; ok {
		return id
	}
	return geckoID
}
