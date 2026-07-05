package fetch

import (
	"bufio"
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

const (
	bgpToolsURL = "https://bgp.tools/table.jsonl"
	userAgent   = "compassvpn-prefix-fetcher bgp.tools"
	maxRetries  = 4
	retryDelay  = 1 * time.Second
)

// BGP route entry with its announcing ASN.
type Prefix struct {
	CIDR netip.Prefix `json:"CIDR"`
	ASN  int          `json:"ASN"`
}

// Downloads the full BGP table with exponential backoff on failures,
// retaining only prefixes announced by ASNs in asnSet.
func fetchWithRetrySimple(client *http.Client, asnSet map[int]bool) ([]Prefix, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		prefixes, err := fetchPrefixesSimple(client, asnSet)
		if err == nil {
			return prefixes, nil
		}

		lastErr = err
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
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	var prefixes []Prefix
	scanner := bufio.NewScanner(resp.Body)

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

// Splits already-filtered prefixes by IP family and sorts each deterministically.
func splitByFamily(prefixes []Prefix) ([]netip.Prefix, []netip.Prefix) {
	var v4, v6 []netip.Prefix

	for _, prefix := range prefixes {
		if prefix.CIDR.Addr().Is4() {
			v4 = append(v4, prefix.CIDR)
		} else if prefix.CIDR.Addr().Is6() {
			v6 = append(v6, prefix.CIDR)
		}
	}

	slices.SortFunc(v4, prefixCompare)
	slices.SortFunc(v6, prefixCompare)

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
