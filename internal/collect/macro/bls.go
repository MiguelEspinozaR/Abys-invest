package macro

import (
	"fmt"
	"strconv"
	"time"
)

// BLSResponse mirrors the v2 timeseries response envelope.
type BLSResponse struct {
	Status       string `json:"status"`
	ResponseTime int    `json:"responseTime"`
	Message      []any  `json:"message"`
	Results      struct {
		Series []BLSRawSeries `json:"series"`
	} `json:"Results"`
}

// BLSRawSeries is one requested series with its data points.
type BLSRawSeries struct {
	SeriesID string     `json:"seriesID"`
	Data     []BLSDatum `json:"data"`
}

// BLSDatum is one monthly observation.
// Period is "M01".."M12"; M13 (annual) is ignored by ParseBLSResponse.
type BLSDatum struct {
	Year       string     `json:"year"`
	Period     string     `json:"period"`
	PeriodName string     `json:"periodName"`
	Value      string     `json:"value"`
	Footnotes  []Footnote `json:"footnotes"`
}

// Footnote is a data footnote (value quality flags).
type Footnote struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// ParseBLSResponse decodes a BLS v2 timeseries payload into flattened
// monthly observations (year, period -> date). Any data point with non-monthly
// period (M13) or non-numeric value is skipped.
func ParseBLSResponse(data []byte) ([]BLSDatum, error) {
	var resp BLSResponse
	if err := decodeJSON(data, &resp); err != nil {
		return nil, err
	}
	if resp.Status == "REQUEST_FAILED" {
		var msg string
		if len(resp.Message) > 0 {
			msg = fmt.Sprintf("%v", resp.Message[0])
		}
		return nil, fmt.Errorf("%w: %s", ErrRequestFailed, msg)
	}
	if resp.Status != "REQUEST_SUCCEEDED" {
		return nil, fmt.Errorf("macro: BLS status desconocido %q", resp.Status)
	}

	var out []BLSDatum
	for _, s := range resp.Results.Series {
		for _, d := range s.Data {
			if len(d.Period) != 3 || d.Period[0] != 'M' {
				continue // M13 (anual) u otras: no son mensuales
			}
			if _, ok := monthNumber(d.Period); !ok {
				continue
			}
			if _, err := strconv.ParseFloat(d.Value, 64); err != nil {
				continue
			}
			out = append(out, d)
		}
	}
	return out, nil
}

// DatumDate converts a BLSDatum to its month-start date ("M01"+"2024" -> 2024-01-01).
func DatumDate(d BLSDatum) (time.Time, error) {
	month, ok := monthNumber(d.Period)
	if !ok {
		return time.Time{}, fmt.Errorf("macro: periodo no mensual %q", d.Period)
	}
	year, err := strconv.Atoi(d.Year)
	if err != nil {
		return time.Time{}, fmt.Errorf("macro: año inválido %q", d.Year)
	}
	return time.Date(year, month, 1, 0, 0, 0, 0, time.UTC), nil
}

// DatumValue parses the string value to float64.
func DatumValue(d BLSDatum) (float64, error) {
	v, err := strconv.ParseFloat(d.Value, 64)
	if err != nil {
		return 0, fmt.Errorf("macro: valor inválido %q: %w", d.Value, err)
	}
	return v, nil
}

func monthNumber(period string) (time.Month, bool) {
	if len(period) != 3 || period[0] != 'M' {
		return 0, false
	}
	n, err := strconv.Atoi(period[1:])
	if err != nil || n < 1 || n > 12 {
		return 0, false
	}
	return time.Month(n), true
}
