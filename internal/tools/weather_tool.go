package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mindex-backend/config"
)

const (
	weatherCacheTTL     = 15 * time.Minute
	weatherProviderTimeout = 2 * time.Second
)

type WeatherData struct {
	City          string       `json:"city"`
	Temperature   float64      `json:"temperature"`
	FeelsLike     float64      `json:"feels_like"`
	Humidity      int          `json:"humidity"`
	Condition     string       `json:"condition"`
	ConditionIcon string       `json:"condition_icon"`
	WindSpeed     float64      `json:"wind_speed"`
	HourlyTemps   []HourlyTemp `json:"hourly_temps"`
}

type HourlyTemp struct {
	Time string  `json:"time"`
	Temp float64 `json:"temp"`
}

type weatherTool struct{}

func NewWeatherTool() Tool { return &weatherTool{} }

func (t *weatherTool) Name() string        { return "weather" }
func (t *weatherTool) Description() string  { return "Lấy thông tin thời tiết hiện tại và dự báo 24h của một thành phố" }
func (t *weatherTool) Category() ToolCategory { return CategoryInformationRetrieval }
func (t *weatherTool) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"city": map[string]interface{}{
				"type":        "string",
				"description": "Tên thành phố (ví dụ: Hà Nội, Ho Chi Minh, Tokyo)",
			},
		},
		"required": []string{"city"},
	}
}
func (t *weatherTool) Timeout() time.Duration        { return 5 * time.Second }
func (t *weatherTool) TierLimit() map[string]int      { return map[string]int{"FREE": 20, "PRO": 100} }
func (t *weatherTool) RequiresConfirmation() bool      { return false }

func (t *weatherTool) Execute(ctx context.Context, execCtx ToolExecutionContext, input json.RawMessage) (ToolResult, error) {
	var params struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{}, fmt.Errorf("tham số không hợp lệ: %v", err)
	}
	city := strings.TrimSpace(params.City)
	if city == "" {
		return ToolResult{}, fmt.Errorf("thiếu tên thành phố")
	}
	if len(city) > 100 {
		return ToolResult{}, fmt.Errorf("tên thành phố quá dài")
	}

	queryHash := HashQuery("weather", strings.ToLower(city))
	data, err := CachedExecute(ctx, "weather", queryHash, CacheScopeGlobal, "", weatherCacheTTL, func() (json.RawMessage, error) {
		return fetchWeather(ctx, city)
	})
	if err != nil {
		return ToolResult{}, err
	}

	richContent, err := json.Marshal(map[string]interface{}{
		"type":      "weather",
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

func fetchWeather(ctx context.Context, city string) (json.RawMessage, error) {
	if key := config.Env.OpenWeatherMapAPIKey; key != "" {
		pCtx, pCancel := context.WithTimeout(ctx, weatherProviderTimeout)
		data, err := fetchOpenWeatherMap(pCtx, city, key)
		pCancel()
		if err == nil {
			return data, nil
		}
	}

	if key := config.Env.WeatherAPIKey; key != "" {
		pCtx2, pCancel2 := context.WithTimeout(ctx, weatherProviderTimeout)
		data, err := fetchWeatherAPI(pCtx2, city, key)
		pCancel2()
		if err == nil {
			return data, nil
		}
	}

	return fetchOpenMeteo(ctx, city)
}

func fetchOpenWeatherMap(ctx context.Context, city, apiKey string) (json.RawMessage, error) {
	encodedCity := url.QueryEscape(city)

	currentURL := fmt.Sprintf("https://api.openweathermap.org/data/2.5/weather?q=%s&appid=%s&units=metric&lang=vi", encodedCity, apiKey)
	currentBody, err := httpGetJSON(ctx, currentURL)
	if err != nil {
		return nil, fmt.Errorf("OpenWeatherMap current: %w", err)
	}

	var current struct {
		Name string `json:"name"`
		Main struct {
			Temp      float64 `json:"temp"`
			FeelsLike float64 `json:"feels_like"`
			Humidity  int     `json:"humidity"`
		} `json:"main"`
		Weather []struct {
			Description string `json:"description"`
			Icon        string `json:"icon"`
		} `json:"weather"`
		Wind struct {
			Speed float64 `json:"speed"`
		} `json:"wind"`
	}
	if err := json.Unmarshal(currentBody, &current); err != nil {
		return nil, fmt.Errorf("OpenWeatherMap parse current: %w", err)
	}

	forecastURL := fmt.Sprintf("https://api.openweathermap.org/data/2.5/forecast?q=%s&appid=%s&units=metric&cnt=8", encodedCity, apiKey)
	forecastBody, err := httpGetJSON(ctx, forecastURL)
	if err != nil {
		return nil, fmt.Errorf("OpenWeatherMap forecast: %w", err)
	}

	var forecast struct {
		List []struct {
			Dt   int64 `json:"dt"`
			Main struct {
				Temp float64 `json:"temp"`
			} `json:"main"`
		} `json:"list"`
	}
	if err := json.Unmarshal(forecastBody, &forecast); err != nil {
		return nil, fmt.Errorf("OpenWeatherMap parse forecast: %w", err)
	}

	hourly := make([]HourlyTemp, 0, len(forecast.List))
	for _, item := range forecast.List {
		hourly = append(hourly, HourlyTemp{
			Time: time.Unix(item.Dt, 0).UTC().Format(time.RFC3339),
			Temp: item.Main.Temp,
		})
	}

	condition := ""
	conditionIcon := ""
	if len(current.Weather) > 0 {
		condition = current.Weather[0].Description
		conditionIcon = current.Weather[0].Icon
	}

	result := WeatherData{
		City:          current.Name,
		Temperature:   current.Main.Temp,
		FeelsLike:     current.Main.FeelsLike,
		Humidity:      current.Main.Humidity,
		Condition:     condition,
		ConditionIcon: conditionIcon,
		WindSpeed:     current.Wind.Speed,
		HourlyTemps:   hourly,
	}
	return json.Marshal(result)
}

func fetchWeatherAPI(ctx context.Context, city, apiKey string) (json.RawMessage, error) {
	encodedCity := url.QueryEscape(city)
	reqURL := fmt.Sprintf("https://api.weatherapi.com/v1/forecast.json?q=%s&key=%s&days=1&aqi=no", encodedCity, apiKey)
	body, err := httpGetJSON(ctx, reqURL)
	if err != nil {
		return nil, fmt.Errorf("WeatherAPI: %w", err)
	}

	var resp struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
		Current struct {
			TempC     float64 `json:"temp_c"`
			FeelsLike float64 `json:"feelslike_c"`
			Humidity  int     `json:"humidity"`
			Condition struct {
				Text string `json:"text"`
				Icon string `json:"icon"`
			} `json:"condition"`
			WindKph float64 `json:"wind_kph"`
		} `json:"current"`
		Forecast struct {
			ForecastDay []struct {
				Hour []struct {
					TimeEpoch int64   `json:"time_epoch"`
					TempC     float64 `json:"temp_c"`
				} `json:"hour"`
			} `json:"forecastday"`
		} `json:"forecast"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("WeatherAPI parse: %w", err)
	}

	var hourly []HourlyTemp
	if len(resp.Forecast.ForecastDay) > 0 {
		hours := resp.Forecast.ForecastDay[0].Hour
		step := len(hours) / 8
		if step < 1 {
			step = 1
		}
		for i := 0; i < len(hours) && len(hourly) < 8; i += step {
			hourly = append(hourly, HourlyTemp{
				Time: time.Unix(hours[i].TimeEpoch, 0).UTC().Format(time.RFC3339),
				Temp: hours[i].TempC,
			})
		}
	}

	result := WeatherData{
		City:          resp.Location.Name,
		Temperature:   resp.Current.TempC,
		FeelsLike:     resp.Current.FeelsLike,
		Humidity:      resp.Current.Humidity,
		Condition:     resp.Current.Condition.Text,
		ConditionIcon: resp.Current.Condition.Icon,
		WindSpeed:     resp.Current.WindKph / 3.6,
		HourlyTemps:   hourly,
	}
	return json.Marshal(result)
}

type geoCoord struct {
	Lat float64
	Lon float64
}

var vietnamCityGeocode = map[string]geoCoord{
	"ha noi":      {21.0285, 105.8542},
	"hanoi":       {21.0285, 105.8542},
	"ho chi minh": {10.8231, 106.6297},
	"hcm":         {10.8231, 106.6297},
	"saigon":      {10.8231, 106.6297},
	"sai gon":     {10.8231, 106.6297},
	"tp hcm":      {10.8231, 106.6297},
	"tphcm":       {10.8231, 106.6297},
	"da nang":     {16.0544, 108.2022},
	"danang":      {16.0544, 108.2022},
	"can tho":     {10.0452, 105.7469},
	"cantho":      {10.0452, 105.7469},
	"hue":         {16.4637, 107.5909},
	"hai phong":   {20.8449, 106.6881},
	"haiphong":    {20.8449, 106.6881},
	"nha trang":   {12.2388, 109.1967},
	"da lat":      {11.9404, 108.4583},
	"dalat":       {11.9404, 108.4583},
	"vung tau":    {10.3460, 107.0843},
	"vungtau":     {10.3460, 107.0843},
	"bien hoa":    {10.9574, 106.8426},
	"bienhoa":     {10.9574, 106.8426},
	"quy nhon":    {13.7563, 109.2297},
	"vinh":        {18.6796, 105.6813},
	"thai nguyen": {21.5928, 105.8442},
	"buon ma thuot": {12.6667, 108.0500},
	"long xuyen":  {10.3863, 105.4352},
	"nam dinh":    {20.4388, 106.1621},
	"phan thiet":  {10.9333, 108.1000},
}

var wmoWeatherDescription = map[int]string{
	0:  "Trời quang",
	1:  "Ít mây",
	2:  "Mây rải rác",
	3:  "Nhiều mây",
	45: "Sương mù",
	48: "Sương mù",
	51: "Mưa phùn nhẹ",
	53: "Mưa phùn",
	55: "Mưa phùn nặng",
	56: "Mưa phùn lạnh",
	57: "Mưa phùn lạnh nặng",
	61: "Mưa nhẹ",
	63: "Mưa vừa",
	65: "Mưa to",
	66: "Mưa lạnh nhẹ",
	67: "Mưa lạnh nặng",
	71: "Tuyết nhẹ",
	73: "Tuyết vừa",
	75: "Tuyết to",
	77: "Hạt tuyết",
	80: "Mưa rào nhẹ",
	81: "Mưa rào vừa",
	82: "Mưa rào to",
	85: "Mưa tuyết nhẹ",
	86: "Mưa tuyết nặng",
	95: "Giông",
	96: "Giông kèm mưa đá nhẹ",
	99: "Giông kèm mưa đá to",
}

var diacriticsReplacer = strings.NewReplacer(
	"à", "a", "á", "a", "ả", "a", "ã", "a", "ạ", "a",
	"ă", "a", "ắ", "a", "ằ", "a", "ẳ", "a", "ẵ", "a", "ặ", "a",
	"â", "a", "ấ", "a", "ầ", "a", "ẩ", "a", "ẫ", "a", "ậ", "a",
	"đ", "d",
	"è", "e", "é", "e", "ẻ", "e", "ẽ", "e", "ẹ", "e",
	"ê", "e", "ế", "e", "ề", "e", "ể", "e", "ễ", "e", "ệ", "e",
	"ì", "i", "í", "i", "ỉ", "i", "ĩ", "i", "ị", "i",
	"ò", "o", "ó", "o", "ỏ", "o", "õ", "o", "ọ", "o",
	"ô", "o", "ố", "o", "ồ", "o", "ổ", "o", "ỗ", "o", "ộ", "o",
	"ơ", "o", "ớ", "o", "ờ", "o", "ở", "o", "ỡ", "o", "ợ", "o",
	"ù", "u", "ú", "u", "ủ", "u", "ũ", "u", "ụ", "u",
	"ư", "u", "ứ", "u", "ừ", "u", "ử", "u", "ữ", "u", "ự", "u",
	"ỳ", "y", "ý", "y", "ỷ", "y", "ỹ", "y", "ỵ", "y",
)

func normalizeCity(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = diacriticsReplacer.Replace(s)
	s = strings.ReplaceAll(s, ".", "")
	return s
}

func fetchOpenMeteo(ctx context.Context, city string) (json.RawMessage, error) {
	normalized := normalizeCity(city)
	coord, ok := vietnamCityGeocode[normalized]
	if !ok {
		geocodeURL := fmt.Sprintf("https://geocoding-api.open-meteo.com/v1/search?name=%s&count=1", url.QueryEscape(city))
		geoBody, err := httpGetJSON(ctx, geocodeURL)
		if err != nil {
			return nil, fmt.Errorf("Open-Meteo geocoding: %w", err)
		}
		var geoResp struct {
			Results []struct {
				Lat float64 `json:"latitude"`
				Lon float64 `json:"longitude"`
			} `json:"results"`
		}
		if err := json.Unmarshal(geoBody, &geoResp); err != nil || len(geoResp.Results) == 0 {
			return nil, fmt.Errorf("Open-Meteo: không tìm thấy thành phố '%s'", city)
		}
		coord = geoCoord{Lat: geoResp.Results[0].Lat, Lon: geoResp.Results[0].Lon}
	}

	forecastURL := fmt.Sprintf(
		"https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m&hourly=temperature_2m&forecast_days=1&timezone=auto",
		coord.Lat, coord.Lon,
	)
	body, err := httpGetJSON(ctx, forecastURL)
	if err != nil {
		return nil, fmt.Errorf("Open-Meteo forecast: %w", err)
	}

	var resp struct {
		Current struct {
			Temperature  float64 `json:"temperature_2m"`
			Humidity     int     `json:"relative_humidity_2m"`
			ApparentTemp float64 `json:"apparent_temperature"`
			WeatherCode  int     `json:"weather_code"`
			WindSpeed    float64 `json:"wind_speed_10m"`
		} `json:"current"`
		Hourly struct {
			Time        []string  `json:"time"`
			Temperature []float64 `json:"temperature_2m"`
		} `json:"hourly"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("Open-Meteo parse: %w", err)
	}

	condition := wmoWeatherDescription[resp.Current.WeatherCode]
	if condition == "" {
		condition = "Không xác định"
	}

	var hourly []HourlyTemp
	if len(resp.Hourly.Time) > 0 {
		step := len(resp.Hourly.Time) / 8
		if step < 1 {
			step = 1
		}
		for i := 0; i < len(resp.Hourly.Time) && len(hourly) < 8; i += step {
			hourly = append(hourly, HourlyTemp{
				Time: resp.Hourly.Time[i],
				Temp: resp.Hourly.Temperature[i],
			})
		}
	}

	result := WeatherData{
		City:          city,
		Temperature:   resp.Current.Temperature,
		FeelsLike:     resp.Current.ApparentTemp,
		Humidity:      resp.Current.Humidity,
		Condition:     condition,
		ConditionIcon: "",
		WindSpeed:     resp.Current.WindSpeed / 3.6,
		HourlyTemps:   hourly,
	}
	return json.Marshal(result)
}

func httpGetJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
