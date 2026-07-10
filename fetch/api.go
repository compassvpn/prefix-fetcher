package fetch

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"go4.org/netipx"
)

// Container for country-specific prefix results.
type PrefixSet struct {
	IPv4 []netip.Prefix
	IPv6 []netip.Prefix
}

// Downloads ASN allocations from all RIRs for the given countries in a single
// pass, returning a map keyed by country code.
func GetASNsForCountries(countries []string) (map[string][]int, error) {
	fetcher := NewMultiRIRASNFetcher()
	return fetcher.FetchASNsForCountries(countries)
}

// GetPrefixesForCountries downloads the BGP table exactly once (filtered to the
// union of all countries' ASNs during the scan) and then carves out each
// country's prefixes, converting IPv4 to /24 blocks. Returns a map keyed by
// country code.
func GetPrefixesForCountries(countryASNs map[string][]int) (map[string]*PrefixSet, error) {
	result := make(map[string]*PrefixSet, len(countryASNs))

	// Union of every country's ASNs: we only need to retain a prefix from the
	// table if some requested country announces it.
	union := make(map[int]bool)
	for _, asns := range countryASNs {
		for _, asn := range asns {
			union[asn] = true
		}
	}

	if len(union) == 0 {
		for country := range countryASNs {
			result[country] = &PrefixSet{}
		}
		return result, nil
	}

	client := &http.Client{Timeout: 30 * time.Second}

	fmt.Println("Downloading BGP table once for all countries...")
	bgpPrefixes, err := fetchWithRetrySimple(client, union)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch BGP data: %w", err)
	}

	for country, asns := range countryASNs {
		asnSet := make(map[int]bool, len(asns))
		for _, asn := range asns {
			asnSet[asn] = true
		}

		ipv4, ipv6 := filterAndSplit(bgpPrefixes, asnSet)
		aggregated, err := aggregatePrefixes(ipv6)
		if err != nil {
			return nil, fmt.Errorf("failed to aggregate IPv6 prefixes for %s: %w", country, err)
		}
		result[country] = &PrefixSet{
			IPv4: convertToIPv4Blocks(ipv4),
			IPv6: aggregated,
		}
	}

	return result, nil
}

// Deduplicates and normalizes IPv4 prefixes into /24 blocks.
func convertToIPv4Blocks(prefixes []netip.Prefix) []netip.Prefix {
	if len(prefixes) == 0 {
		return nil
	}

	blockSet := make(map[netip.Prefix]bool)

	for _, prefix := range prefixes {
		if !prefix.Addr().Is4() {
			continue
		}
		for _, block := range splitToBlocks(prefix) {
			blockSet[block] = true
		}
	}

	result := make([]netip.Prefix, 0, len(blockSet))
	for block := range blockSet {
		result = append(result, block)
	}

	slices.SortFunc(result, prefixCompare)
	return result
}

// Aggregates prefixes into the minimal set covering exactly the same
// addresses: duplicates and covered prefixes are dropped and adjacent siblings
// merged. Used for IPv6, where the fixed-block scheme applied to IPv4 does not
// transfer: allocations are too sparse for any uniform block size (splitting
// CN to /48s yields ~1.9 billion blocks, while masking to anything coarser
// swallows address space announced by other networks). Returned prefixes are
// non-overlapping and sorted in address order.
func aggregatePrefixes(prefixes []netip.Prefix) ([]netip.Prefix, error) {
	if len(prefixes) == 0 {
		return nil, nil
	}

	var builder netipx.IPSetBuilder
	for _, prefix := range prefixes {
		builder.AddPrefix(prefix)
	}

	set, err := builder.IPSet()
	if err != nil {
		return nil, fmt.Errorf("failed to build prefix set: %w", err)
	}

	return set.Prefixes(), nil
}

// Breaks down larger prefixes into /24 chunks for consistency. IPv4 is 32-bit,
// so plain uint32 arithmetic suffices (no big.Int needed).
func splitToBlocks(prefix netip.Prefix) []netip.Prefix {
	if prefix.Bits() >= 24 {
		// Already /24 or smaller - just align to /24 boundary
		bytes := prefix.Addr().As4()
		bytes[3] = 0
		return []netip.Prefix{netip.PrefixFrom(netip.AddrFrom4(bytes), 24)}
	}

	// Split larger blocks (e.g., /16, /20) into multiple /24s. The base cannot
	// overflow: base + (blockCount-1)*256 stays within the prefix's range.
	blockCount := 1 << (24 - prefix.Bits())
	blocks := make([]netip.Prefix, blockCount)

	base := ipToUint32(prefix.Addr())
	for i := 0; i < blockCount; i++ {
		blocks[i] = netip.PrefixFrom(uint32ToIP(base+uint32(i)*256), 24)
	}

	return blocks
}

func ipToUint32(ip netip.Addr) uint32 {
	bytes := ip.As4()
	return binary.BigEndian.Uint32(bytes[:])
}

func uint32ToIP(v uint32) netip.Addr {
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], v)
	return netip.AddrFrom4(bytes)
}

// Writes results to standard format files with progress feedback.
func SavePrefixesToFiles(country string, prefixes *PrefixSet) error {
	countryLower := strings.ToLower(country)

	ipv4File := fmt.Sprintf("%s_prefixes_v4.txt", countryLower)
	if err := writePrefixesToFile(ipv4File, prefixes.IPv4); err != nil {
		return fmt.Errorf("failed to save IPv4 prefixes: %w", err)
	}
	fmt.Printf("IPv4 /24 blocks written to: %s (%d entries)\n", ipv4File, len(prefixes.IPv4))

	ipv6File := fmt.Sprintf("%s_prefixes_v6.txt", countryLower)
	if err := writePrefixesToFile(ipv6File, prefixes.IPv6); err != nil {
		return fmt.Errorf("failed to save IPv6 prefixes: %w", err)
	}
	fmt.Printf("IPv6 prefixes written to: %s (%d entries)\n", ipv6File, len(prefixes.IPv6))

	return nil
}

func writePrefixesToFile(filename string, prefixes []netip.Prefix) error {
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Buffer writes: prefix lists can reach ~1M+ /24 blocks (e.g. CN), and one
	// syscall per line is needlessly slow.
	writer := bufio.NewWriter(file)

	for _, prefix := range prefixes {
		if _, err := writer.WriteString(prefix.String() + "\n"); err != nil {
			return fmt.Errorf("failed to write prefix: %w", err)
		}
	}

	if err := writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush prefixes: %w", err)
	}

	return nil
}
