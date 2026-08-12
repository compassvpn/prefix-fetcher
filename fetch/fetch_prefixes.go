package fetch

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	bgpToolsURL = "https://bgp.tools/table.jsonl"
	userAgent   = "compassvpn-prefix-fetcher bgp.tools"
	maxRetries  = 4
	retryDelay  = 1 * time.Second

	// Cap for a scanned line. bufio.Scanner aborts the whole read on lines
	// over its 64KB default; records are tiny, so 1 MiB is safe headroom.
	maxLineSize = 1 << 20
)

// A non-2xx HTTP response, carrying the code so the retry logic can decide
// whether another attempt is worthwhile.
type httpStatusError struct {
	code   int
	status string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.status)
}

// Reports whether an error is worth another attempt. Network errors and 5xx
// responses are transient; among 4xx only 429 and 408 are.
func retriable(err error) bool {
	var se *httpStatusError
	if !errors.As(err, &se) {
		return true // non-HTTP errors (network, read, parse) are transient
	}

	switch se.code {
	case http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	}
	return se.code < 400 || se.code >= 500
}

// BGP route entry with its announcing ASN.
type Prefix struct {
	CIDR netip.Prefix `json:"CIDR"`
	ASN  int          `json:"ASN"`
}

// Downloads the BGP table with linear backoff, retaining only prefixes
// announced by ASNs in asnSet.
func fetchWithRetrySimple(client *http.Client, asnSet map[int]bool) ([]Prefix, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		prefixes, err := fetchPrefixesSimple(client, asnSet)
		if err == nil {
			return prefixes, nil
		}

		lastErr = err
		if !retriable(err) {
			return nil, fmt.Errorf("non-retriable error: %w", err)
		}
		if attempt < maxRetries {
			delay := time.Duration(attempt) * retryDelay
			time.Sleep(delay)
		}
	}

	return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries, lastErr)
}

// Streams the JSONL BGP table, keeping only prefixes whose ASN is in asnSet
// so the full table is never retained in memory.
func fetchPrefixesSimple(client *http.Client, asnSet map[int]bool) ([]Prefix, error) {
	req, err := http.NewRequest("GET", bgpToolsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{code: resp.StatusCode, status: resp.Status}
	}

	var prefixes []Prefix
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var prefix Prefix
		if err := json.Unmarshal([]byte(line), &prefix); err != nil {
			continue // Skip malformed lines
		}

		if !asnSet[prefix.ASN] {
			continue
		}

		prefixes = append(prefixes, prefix)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return prefixes, nil
}

// Selects prefixes announced by ASNs in asnSet and splits them by IP family.
// Output is neither sorted nor deduplicated; callers handle both.
func filterAndSplit(prefixes []Prefix, asnSet map[int]bool) ([]netip.Prefix, []netip.Prefix) {
	var v4, v6 []netip.Prefix

	for _, prefix := range prefixes {
		if !asnSet[prefix.ASN] {
			continue
		}

		// Mask at ingestion: the feed is not guaranteed canonical, and
		// downstream /24 splitting assumes host bits are zero.
		cidr := prefix.CIDR.Masked()
		if cidr.Addr().Is4() {
			v4 = append(v4, cidr)
		} else if cidr.Addr().Is6() {
			v6 = append(v6, cidr)
		}
	}

	return v4, v6
}

// Orders prefixes deterministically.
func prefixCompare(a, b netip.Prefix) int {
	if c := cmp.Compare(a.Addr().BitLen(), b.Addr().BitLen()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Bits(), b.Bits()); c != 0 {
		return c
	}
	return a.Addr().Compare(b.Addr())
}
