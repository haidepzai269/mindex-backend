package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWeatherToolMetadata(t *testing.T) {
	tool := NewWeatherTool()
	if tool.Name() != "weather" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "weather")
	}
	if tool.Category() != CategoryInformationRetrieval {
		t.Errorf("Category() = %q, want information_retrieval", tool.Category())
	}
	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters missing properties")
	}
	if _, ok := props["city"]; !ok {
		t.Error("Parameters missing 'city' property")
	}
	if tool.RequiresConfirmation() {
		t.Error("weather tool should not require confirmation")
	}
	if tool.TierLimit() == nil {
		t.Error("weather tool should have tier limits")
	}
}

func TestWeatherToolRejectsEmptyCity(t *testing.T) {
	tool := NewWeatherTool()
	_, err := tool.Execute(context.TODO(), ToolExecutionContext{}, json.RawMessage(`{"city":""}`))
	if err == nil {
		t.Error("expected error for empty city")
	}
}

func TestWeatherToolRejectsMissingCity(t *testing.T) {
	tool := NewWeatherTool()
	_, err := tool.Execute(context.TODO(), ToolExecutionContext{}, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for missing city")
	}
}

func TestWeatherToolRejectsInvalidJSON(t *testing.T) {
	tool := NewWeatherTool()
	_, err := tool.Execute(context.TODO(), ToolExecutionContext{}, json.RawMessage(`not json`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseOpenWeatherMapResponse(t *testing.T) {
	currentJSON := `{
		"name": "Hanoi",
		"main": {"temp": 32.5, "feels_like": 36.1, "humidity": 75},
		"weather": [{"description": "mây cụm", "icon": "03d"}],
		"wind": {"speed": 3.2}
	}`
	forecastJSON := `{
		"list": [
			{"dt": 1719446400, "main": {"temp": 28.1}},
			{"dt": 1719457200, "main": {"temp": 27.5}},
			{"dt": 1719468000, "main": {"temp": 29.0}}
		]
	}`

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
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		t.Fatalf("parse current: %v", err)
	}
	if current.Main.Temp != 32.5 {
		t.Errorf("temp = %f, want 32.5", current.Main.Temp)
	}
	if current.Weather[0].Description != "mây cụm" {
		t.Errorf("condition = %q, want %q", current.Weather[0].Description, "mây cụm")
	}

	var forecast struct {
		List []struct {
			Dt   int64 `json:"dt"`
			Main struct {
				Temp float64 `json:"temp"`
			} `json:"main"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(forecastJSON), &forecast); err != nil {
		t.Fatalf("parse forecast: %v", err)
	}
	if len(forecast.List) != 3 {
		t.Errorf("forecast list length = %d, want 3", len(forecast.List))
	}
}

func TestParseWeatherAPIResponse(t *testing.T) {
	respJSON := `{
		"location": {"name": "Ho Chi Minh City"},
		"current": {
			"temp_c": 33.0,
			"feelslike_c": 38.5,
			"humidity": 65,
			"condition": {"text": "Partly cloudy", "icon": "//cdn.weatherapi.com/116.png"},
			"wind_kph": 11.5
		},
		"forecast": {
			"forecastday": [{
				"hour": [
					{"time_epoch": 1719446400, "temp_c": 28.0},
					{"time_epoch": 1719450000, "temp_c": 27.5},
					{"time_epoch": 1719453600, "temp_c": 27.0},
					{"time_epoch": 1719457200, "temp_c": 26.5}
				]
			}]
		}
	}`

	var resp struct {
		Location struct {
			Name string `json:"name"`
		} `json:"location"`
		Current struct {
			TempC    float64 `json:"temp_c"`
			Humidity int     `json:"humidity"`
		} `json:"current"`
	}
	if err := json.Unmarshal([]byte(respJSON), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Location.Name != "Ho Chi Minh City" {
		t.Errorf("city = %q, want %q", resp.Location.Name, "Ho Chi Minh City")
	}
	if resp.Current.TempC != 33.0 {
		t.Errorf("temp = %f, want 33.0", resp.Current.TempC)
	}
}

func TestVietnameseCityNameEncoding(t *testing.T) {
	cities := []string{"Hà Nội", "Hồ Chí Minh", "Đà Nẵng", "Sài Gòn"}
	for _, city := range cities {
		lower := strings.ToLower(city)
		hash := HashQuery("weather", lower)
		if len(hash) != 16 {
			t.Errorf("HashQuery(%q) length = %d, want 16", city, len(hash))
		}
	}
}
