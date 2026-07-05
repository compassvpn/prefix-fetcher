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

	// maxLineSize caps a single scanned line. bufio.Scanner defaults to 64KB
	// and aborts the whole read on a longer line; BGP/RIR records are tiny, but
	// 1 MiB of headroom makes an unusually long line a non-issue.
	maxLineSize = 1 << 20
)

// httpStatusError represents a non-2xx HTTP response, carrying the code so the
// retry logic can decide whether another attempt is worthwhile.
type httpStatusError struct {
	code   int
	status string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.status)
}

// retriable reports whether an error is worth another attempt. Network errors
// and 5xx responses are transient. Among 4xx responses, only 429 (Too Many
// Requests) and 408 (Request Timeout) are worth retrying — the rest (404, 403,
// 400, ...) are permanent and retrying just wastes time.
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

// Downloads the full BGP table with linear backoff on failures (delay grows
// by retryDelay each attempt), retaining only prefixes announced by ASNs in
// asnSet.
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

// Streams and parses JSONL BGP data from bgp.tools, keeping only prefixes
// whose ASN is in asnSet so the full table is never retained in memory.
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
	defer resp.Body.Close()

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

		// Stream-filter: discard prefixes from ASNs we don't care about so
		// the ~1M-row table never accumulates in memory.
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
// Used to carve a single country's prefixes out of the shared (union-filtered)
// table. Neither family is sorted or deduplicated here: v4 is handled by
// convertToIPv4Blocks and v6 by dedupPrefixes, both of which dedup and sort.
func filterAndSplit(prefixes []Prefix, asnSet map[int]bool) ([]netip.Prefix, []netip.Prefix) {
	var v4, v6 []netip.Prefix

	for _, prefix := range prefixes {
		if !asnSet[prefix.ASN] {
			continue
		}

		if prefix.CIDR.Addr().Is4() {
			v4 = append(v4, prefix.CIDR)
		} else if prefix.CIDR.Addr().Is6() {
			v6 = append(v6, prefix.CIDR)
		}
	}

	return v4, v6
}

// Comparison for deterministic prefix ordering.
func prefixCompare(a, b netip.Prefix) int {
	if c := cmp.Compare(a.Addr().BitLen(), b.Addr().BitLen()); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Bits(), b.Bits()); c != 0 {
		return c
	}
	return a.Addr().Compare(b.Addr())
}
