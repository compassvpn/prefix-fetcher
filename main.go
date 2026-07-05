package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"prefix-fetcher/fetch"
)

func main() {
	var (
		fetchIR  = flag.Bool("fetch-ir", false, "Fetch IP prefixes for Iran (IR)")
		fetchCN  = flag.Bool("fetch-cn", false, "Fetch IP prefixes for China (CN)")
		fetchRU  = flag.Bool("fetch-ru", false, "Fetch IP prefixes for Russia (RU)")
		help     = flag.Bool("h", false, "Show help")
		helpLong = flag.Bool("help", false, "Show help")
	)
	flag.Parse()

	if *help || *helpLong {
		showHelp()
		return
	}

	// Collect requested countries in a stable order.
	var countries []string
	if *fetchIR {
		countries = append(countries, "IR")
	}
	if *fetchCN {
		countries = append(countries, "CN")
	}
	if *fetchRU {
		countries = append(countries, "RU")
	}

	if len(countries) == 0 {
		fmt.Fprintf(os.Stderr, "Error: Please specify one of --fetch-ir, --fetch-cn, or --fetch-ru\n\n")
		showHelp()
		os.Exit(1)
	}

	if err := fetchAndSavePrefixes(countries); err != nil {
		log.Fatalf("Failed to fetch prefixes: %v", err)
	}
}

// fetchAndSavePrefixes fetches ASNs and prefixes for the given countries and
// saves them to files. The shared RIR delegated files and the BGP table are
// each downloaded only once, regardless of how many countries are requested.
func fetchAndSavePrefixes(countries []string) error {
	fmt.Printf("Fetching prefixes for %s...\n", strings.Join(countries, ", "))

	// Get ASNs from all RIRs (single pass).
	asnsByCountry, err := fetch.GetASNsForCountries(countries)
	if err != nil {
		return fmt.Errorf("failed to get ASNs: %w", err)
	}

	// Fetch BGP prefixes (single download for all countries).
	prefixesByCountry, err := fetch.GetPrefixesForCountries(asnsByCountry)
	if err != nil {
		return fmt.Errorf("failed to get prefixes: %w", err)
	}

	// Save each country's results.
	for _, country := range countries {
		prefixes := prefixesByCountry[country]
		fmt.Printf("Found %d IPv4 and %d IPv6 prefixes for %s\n", len(prefixes.IPv4), len(prefixes.IPv6), country)

		if err := fetch.SavePrefixesToFiles(country, prefixes); err != nil {
			return fmt.Errorf("failed to save prefixes for %s: %w", country, err)
		}
	}

	fmt.Println("Prefixes saved successfully")
	return nil
}

func showHelp() {
	fmt.Println("prefix-fetcher - Fetch IP prefixes for countries")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  ./prefix-fetcher --fetch-ir   Fetch prefixes for Iran")
	fmt.Println("  ./prefix-fetcher --fetch-cn   Fetch prefixes for China")
	fmt.Println("  ./prefix-fetcher --fetch-ru   Fetch prefixes for Russia")
	fmt.Println("  ./prefix-fetcher -h, --help   Show this help")
	fmt.Println()
	fmt.Println("Output files:")
	fmt.Println("  ir_prefixes_v4.txt, ir_prefixes_v6.txt")
	fmt.Println("  cn_prefixes_v4.txt, cn_prefixes_v6.txt")
	fmt.Println("  ru_prefixes_v4.txt, ru_prefixes_v6.txt")
}
