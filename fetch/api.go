package fetch

import (
	"bufio"
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"
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
		result[country] = &PrefixSet{
			IPv4: convertToIPv4Blocks(ipv4),
			IPv6: ipv6,
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

// Breaks down larger prefixes into /24 chunks for consistency.
func splitToBlocks(prefix netip.Prefix) []netip.Prefix {
	if prefix.Bits() >= 24 {
		// Already /24 or smaller - just align to /24 boundary
		addr := prefix.Addr()
		bytes := addr.As4()
		bytes[3] = 0
		baseAddr, _ := netip.AddrFromSlice(bytes[:])
		block := netip.PrefixFrom(baseAddr, 24)
		return []netip.Prefix{block}
	}

	// Split larger blocks (e.g., /16, /20) into multiple /24s
	blockCount := 1 << (24 - prefix.Bits())
	blocks := make([]netip.Prefix, blockCount)

	baseInt := ipToInt(prefix.Addr())
	increment := big.NewInt(256)

	for i := 0; i < blockCount; i++ {
		ip := intToIP(baseInt)
		blocks[i] = netip.PrefixFrom(ip, 24)
		baseInt.Add(baseInt, increment)
	}

	return blocks
}

func ipToInt(ip netip.Addr) *big.Int {
	return big.NewInt(0).SetBytes(ip.AsSlice())
}

func intToIP(ipInt *big.Int) netip.Addr {
	bytes := make([]byte, 4)
	ipInt.FillBytes(bytes)
	addr, _ := netip.AddrFromSlice(bytes)
	return addr
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
