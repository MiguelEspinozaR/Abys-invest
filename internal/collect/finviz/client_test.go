package finviz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const quotePageFixture = `<!DOCTYPE html>
<html><head><title>AAPL Stock Quote</title></head><body>
<table>
<tr><td class="fullview-links">
<a class="tab-link" href="news.ashx">News</a>
</td></tr>
</table>
<table>
<tr>
<td class="snapshot-table2">
<div class="quote-header_categories">
<a href="screener?v=111&amp;f=sec_technology" class="quote-header_category">Technology</a>
<a href="screener?v=111&amp;f=ind_consumerelectronics" class="quote-header_category" title="Consumer Electronics">Consumer Electronics</a>
</div>
</td>
</tr>
</table>
</body></html>`

func TestParseQuotePage(t *testing.T) {
	info, err := parseQuotePage([]byte(quotePageFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Sector != "Technology" {
		t.Fatalf("sector esperado %q, got %q", "Technology", info.Sector)
	}
	if info.Industry != "Consumer Electronics" {
		t.Fatalf("industry esperado %q, got %q", "Consumer Electronics", info.Industry)
	}
}

func TestParseQuotePageMissing(t *testing.T) {
	if _, err := parseQuotePage([]byte("<html><body>sin categorías</body></html>")); !errors.Is(err, ErrNoSectorInfo) {
		t.Fatalf("se esperaba ErrNoSectorInfo, got %v", err)
	}
}

func TestGetSectorInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "AAPL" {
			http.Error(w, "ticker no enviado", http.StatusBadRequest)
			return
		}
		w.Write([]byte(quotePageFixture))
	}))
	defer srv.Close()

	c := NewClient(WithQuoteURL(srv.URL))
	info, err := c.GetSectorInfo(context.Background(), "aapl")
	if err != nil {
		t.Fatalf("GetSectorInfo: %v", err)
	}
	if info.Sector != "Technology" || !strings.Contains(info.Industry, "Consumer") {
		t.Fatalf("info incorrecto: %+v", info)
	}
}

func TestGetSectorInfoHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := NewClient(WithQuoteURL(srv.URL))
	if _, err := c.GetSectorInfo(context.Background(), "AAPL"); err == nil {
		t.Fatal("se esperaba error HTTP")
	}
}
