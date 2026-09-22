package yahoo

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// ChartResponse mirrors the v8 chart API JSON envelope.
type ChartResponse struct {
	Chart struct {
		Result []ChartResultRaw `json:"result"`
		Error  *ChartAPIError   `json:"error"`
	} `json:"chart"`
}

// ChartAPIError is the error object returned by the chart API.
type ChartAPIError struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// ChartResultRaw is one result entry of the chart payload (usually one).
type ChartResultRaw struct {
	Meta struct {
		Symbol             string  `json:"symbol"`
		RegularMarketPrice float64 `json:"regularMarketPrice"`
		Currency           string  `json:"currency"`
	} `json:"meta"`
	Timestamp  []int64 `json:"timestamp"`
	Indicators struct {
		Quote []struct {
			Open   []*float64 `json:"open"`
			High   []*float64 `json:"high"`
			Low    []*float64 `json:"low"`
			Close  []*float64 `json:"close"`
			Volume []*int64   `json:"volume"`
		} `json:"quote"`
		AdjClose []struct {
			AdjClose []*float64 `json:"adjclose"`
		} `json:"adjclose"`
	} `json:"indicators"`
}

// OHLCVBar is one daily price bar (adjusted close may fall back to close).
type OHLCVBar struct {
	Date          time.Time `json:"date"`
	Open          *float64  `json:"open,omitempty"`
	High          *float64  `json:"high,omitempty"`
	Low           *float64  `json:"low,omitempty"`
	Close         *float64  `json:"close,omitempty"`
	AdjustedClose float64   `json:"adjusted_close"`
	Volume        *int64    `json:"volume,omitempty"`
}

// ChartResult is the parsed, ready-to-use output of a chart API call.
type ChartResult struct {
	Symbol       string
	Currency     string
	CurrentPrice float64
	Bars         []OHLCVBar
}

// ParseChartResponse decodes a v8 chart payload into a ChartResult. It fails
// when the envelope reports an API error, has no results, or no quote arrays
// are present.
func ParseChartResponse(data []byte) (*ChartResult, error) {
	var resp ChartResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("yahoo: parse chart response: %w", err)
	}
	if resp.Chart.Error != nil {
		return nil, fmt.Errorf("yahoo: chart API error: %s (%s)",
			resp.Chart.Error.Description, resp.Chart.Error.Code)
	}
	if len(resp.Chart.Result) == 0 {
		return nil, ErrEmptyResult
	}

	raw := resp.Chart.Result[0]
	if len(raw.Indicators.Quote) == 0 {
		return nil, fmt.Errorf("yahoo: chart response sin indicators.quote")
	}
	quote := raw.Indicators.Quote[0]

	// Adjusted close is optional; fallback to close when absent.
	var adj []*float64
	if len(raw.Indicators.AdjClose) > 0 {
		adj = raw.Indicators.AdjClose[0].AdjClose
	}

	// Uniform lengths guard: timestamp lengths must match quote arrays.
	n := len(raw.Timestamp)
	if len(quote.Close) < n {
		n = len(quote.Close)
	}

	out := &ChartResult{
		Symbol:       raw.Meta.Symbol,
		Currency:     raw.Meta.Currency,
		CurrentPrice: raw.Meta.RegularMarketPrice,
		Bars:         make([]OHLCVBar, 0, n),
	}
	for i := 0; i < n; i++ {
		bar := OHLCVBar{
			Date:   time.Unix(raw.Timestamp[i], 0).UTC(),
			Open:   quote.Open[i],
			High:   quote.High[i],
			Low:    quote.Low[i],
			Close:  quote.Close[i],
			Volume: quote.Volume[i],
		}
		// adjusted close: prefer adjclose array; fallback to close.
		if i < len(adj) && adj[i] != nil {
			bar.AdjustedClose = *adj[i]
		} else if quote.Close[i] != nil {
			bar.AdjustedClose = *quote.Close[i]
		}
		out.Bars = append(out.Bars, bar)
	}
	return out, nil
}

// ValidBar reports whether the bar carries a usable close price (finite > 0),
// used by the ingestion path to skip broken rows.
func ValidBar(b OHLCVBar) bool {
	return b.Close != nil && !math.IsNaN(*b.Close) && !math.IsInf(*b.Close, 0) && *b.Close > 0
}
