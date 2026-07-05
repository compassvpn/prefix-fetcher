package fetch

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Registry endpoint with its official delegated file URL.
type RIR struct {
	Name string
	URL  string
}

// Parsed entry from an RIR's pipe-delimited allocation file.
type DelegatedRecord struct {
	Registry string // RIR name
	CC       string // Country code
	Type     string // Record type (asn, ipv4, ipv6)
	Start    string // Start value
	Value    string // Count or size
	Date     string // Allocation date
	Status   string // Allocation status
}

// Downloads and parses ASN data from multiple RIR sources.
type MultiRIRASNFetcher struct {
	httpClient *http.Client
}

// Well-known RIR delegated file URLs
var (
	RIPE_NCC = RIR{
		Name: "RIPE NCC",
		URL:  "https://ftp.ripe.net/ripe/stats/delegated-ripencc-latest",
	}
	APNIC = RIR{
		Name: "APNIC",
		URL:  "https://ftp.apnic.net/stats/apnic/delegated-apnic-latest",
	}
	ARIN = RIR{
		Name: "ARIN",
		URL:  "https://ftp.arin.net/pub/stats/arin/delegated-arin-extended-latest",
	}
	LACNIC = RIR{
		Name: "LACNIC",
		URL:  "https://ftp.lacnic.net/pub/stats/lacnic/delegated-lacnic-latest",
	}
	AFRINIC = RIR{
		Name: "AFRINIC",
		URL:  "https://ftp.afrinic.net/pub/stats/afrinic/delegated-afrinic-latest",
	}
)

// Maps countries to their primary RIR for reference (now fetches from all RIRs for completeness).
var CountryToRIR = map[string]RIR{
	"IR": RIPE_NCC, // Iran - Middle East (RIPE NCC coverage)
	"CN": APNIC,    // China - Asia-Pacific (APNIC coverage)
	"RU": RIPE_NCC, // Russia - Europe/Central Asia (RIPE NCC coverage)
}

func NewMultiRIRASNFetcher() *MultiRIRASNFetcher {
	return &MultiRIRASNFetcher{
		httpClient: &http.Client{
			Timeout: 120 * time.Second, // Increased timeout for fetching from all RIRs
		},
	}
}

// FetchASNsForCountries downloads every RIR delegated file exactly once and
// extracts ASNs for all requested countries in a single pass, returning a map
// keyed by country code. This avoids re-downloading the (shared) RIR files once
// per country.
func (f *MultiRIRASNFetcher) FetchASNsForCountries(countryCodes []string) (map[string][]int, error) {
	// Get all RIRs for comprehensive coverage
	rirs := []RIR{RIPE_NCC, APNIC, ARIN, LACNIC, AFRINIC}

	fmt.Printf("Fetching ASNs for %s from all RIRs for comprehensive coverage\n", strings.Join(countryCodes, ", "))

	// Per-country ASN sets for deduplication across RIRs.
	asnSets := make(map[string]map[int]bool, len(countryCodes))
	for _, cc := range countryCodes {
		asnSets[cc] = make(map[int]bool)
	}

	// Track which RIRs were fetched so we can enforce the primary-RIR guard.
	fetched := make(map[RIR]bool, len(rirs))

	for _, rir := range rirs {
		fmt.Printf("Checking %s (%s)...\n", rir.Name, rir.URL)

		records, err := f.fetchDelegatedRecords(rir.URL)
		if err != nil {
			fmt.Printf("Warning: Failed to fetch from %s: %v\n", rir.Name, err)
			continue // Continue with other RIRs even if one fails
		}
		fetched[rir] = true

		for _, cc := range countryCodes {
			asns := f.extractASNsForCountry(records, cc)
			fmt.Printf("Found %d ASNs for %s from %s\n", len(asns), cc, rir.Name)

			for _, asn := range asns {
				asnSets[cc][asn] = true
			}
		}
	}

	result := make(map[string][]int, len(countryCodes))
	for _, cc := range countryCodes {
		// The primary RIR holds the bulk of a country's allocations. If it
		// cannot be fetched we must abort rather than emit a near-empty list,
		// which would otherwise be published and prune known-good releases.
		if primary, hasPrimary := CountryToRIR[cc]; hasPrimary && !fetched[primary] {
			return nil, fmt.Errorf("primary RIR %s for %s could not be fetched after retries; aborting to avoid publishing incomplete data", primary.Name, cc)
		}

		asns := make([]int, 0, len(asnSets[cc]))
		for asn := range asnSets[cc] {
			asns = append(asns, asn)
		}
		sort.Ints(asns)

		if len(asns) == 0 {
			return nil, fmt.Errorf("no ASNs found for %s; refusing to continue with empty data", cc)
		}

		fmt.Printf("Total unique ASNs for %s across all RIRs: %d\n", cc, len(asns))
		result[cc] = asns
	}

	return result, nil
}

// Fetches an RIR delegated file with retries and linear backoff, mirroring
// the BGP fetch behaviour so a transient upstream failure does not silently
// drop a country's ASNs.
func (f *MultiRIRASNFetcher) fetchDelegatedRecords(url string) ([]DelegatedRecord, error) {
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		records, err := f.fetchDelegatedRecordsOnce(url)
		if err == nil {
			return records, nil
		}

		lastErr = err
		if !retriable(err) {
			return nil, fmt.Errorf("non-retriable error: %w", err)
		}
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * retryDelay)
		}
	}

	return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries, lastErr)
}

func (f *MultiRIRASNFetcher) fetchDelegatedRecordsOnce(url string) ([]DelegatedRecord, error) {
	resp, err := f.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{code: resp.StatusCode, status: resp.Status}
	}

	return f.parseDelegatedFile(resp.Body)
}

// Parses the standard RIR delegated file format (pipe-delimited).
func (f *MultiRIRASNFetcher) parseDelegatedFile(reader io.Reader) ([]DelegatedRecord, error) {
	var records []DelegatedRecord
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Skip version and summary lines
		if strings.Contains(line, "|version|") || strings.Contains(line, "|summary|") {
			continue
		}

		record, err := f.parseDelegatedRecord(line)
		if err != nil {
			// Skip malformed lines rather than failing completely
			continue
		}

		records = append(records, record)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading delegated file: %w", err)
	}

	return records, nil
}

func (f *MultiRIRASNFetcher) parseDelegatedRecord(line string) (DelegatedRecord, error) {
	parts := strings.Split(line, "|")
	if len(parts) < 7 {
		return DelegatedRecord{}, fmt.Errorf("invalid record format: %s", line)
	}

	return DelegatedRecord{
		Registry: parts[0],
		CC:       parts[1],
		Type:     parts[2],
		Start:    parts[3],
		Value:    parts[4],
		Date:     parts[5],
		Status:   parts[6],
	}, nil
}

// Extracts valid public ASNs from delegated records, expanding ranges.
func (f *MultiRIRASNFetcher) extractASNsForCountry(records []DelegatedRecord, countryCode string) []int {
	var asns []int

	for _, record := range records {
		// Only process ASN records for the specified country
		if record.Type != "asn" || record.CC != countryCode {
			continue
		}

		// Skip if status indicates it's not a proper allocation
		if record.Status == "reserved" || record.Status == "available" {
			continue
		}

		startASN, err := strconv.Atoi(record.Start)
		if err != nil {
			continue
		}

		count, err := strconv.Atoi(record.Value)
		if err != nil {
			continue
		}

		// Expand ASN ranges and filter out private/reserved numbers
		for i := 0; i < count; i++ {
			asn := startASN + i
			if f.isValidPublicASN(asn) {
				asns = append(asns, asn)
			}
		}
	}

	return asns
}

// Validates ASN against IANA reservations and private ranges.
func (f *MultiRIRASNFetcher) isValidPublicASN(asn int) bool {
	// Filter out reserved and private ASN ranges
	// See: https://www.iana.org/assignments/as-numbers/as-numbers.xhtml

	if asn == 0 {
		return false // Reserved
	}
	if asn >= 64512 && asn <= 65534 {
		return false // Private Use 16-bit
	}
	if asn == 65535 {
		return false // Reserved
	}
	if asn >= 4200000000 && asn <= 4294967294 {
		return false // Private Use 32-bit
	}
	if asn == 4294967295 {
		return false // Reserved
	}

	// Valid public ASN ranges:
	// 1-64511 (16-bit public)
	// 131072-4199999999 (32-bit public, excluding private ranges)

	if asn >= 1 && asn <= 64511 {
		return true
	}
	if asn >= 131072 && asn <= 4199999999 {
		return true
	}

	return false
}
