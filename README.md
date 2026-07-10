# Prefix Fetcher

**Prefix Fetcher** is a Go-based tool for fetching country-specific IP prefixes from BGP data. Dynamically fetches ASN lists from Regional Internet Registry (RIR) delegated files and filters BGP prefixes accordingly.

## Features

- **Dynamic ASN Fetching**: Automatically downloads the latest ASN allocations from RIR delegated files
  - Iran (IR): Uses RIPE NCC delegated data
  - China (CN): Uses APNIC delegated data
  - Russia (RU): Uses RIPE NCC delegated data
- **Comprehensive Coverage**: Fetches from all RIRs for complete ASN discovery
- **BGP Data**: Fetches IP prefixes from [bgp.tools](https://bgp.tools/table.jsonl)
- **IPv4 /24 Blocks**: Converts IPv4 prefixes to /24 blocks for efficient processing
- **IPv6 Aggregation**: Merges IPv6 prefixes into the minimal set covering exactly the same addresses
- **Clean & Simple**: No caching, fresh data on every run
- **Automatic retry logic** with linear backoff
- **Duplicate removal**: Automatically deduplicates ASNs found across multiple RIRs

## Prerequisites

- **Go (>=1.24)**

## Build

1. Clone the repository:

    ```sh
    git clone https://github.com/compassvpn/prefix-fetcher.git
    cd prefix-fetcher
    ```

2. Build the application:

    ```sh
    go build -o prefix-fetcher
    ```

## Usage

```sh
./prefix-fetcher [OPTIONS]
```

### Available Options

| Option        | Description                                    |
|---------------|------------------------------------------------|
| `--fetch-ir`  | Fetch Iranian IP prefixes                      |
| `--fetch-cn`  | Fetch Chinese IP prefixes                      |
| `--fetch-ru`  | Fetch Russian IP prefixes                      |
| `-h, --help`  | Show help information                          |

### Examples

- Fetch Iranian IP prefixes:

  ```sh
  ./prefix-fetcher --fetch-ir
  ```

- Fetch Chinese IP prefixes:

  ```sh
  ./prefix-fetcher --fetch-cn
  ```

- Fetch Russian IP prefixes:

  ```sh
  ./prefix-fetcher --fetch-ru
  ```

- Fetch multiple countries:

  ```sh
  ./prefix-fetcher --fetch-ir --fetch-cn --fetch-ru
  ```

- Show help:

  ```sh
  ./prefix-fetcher --help
  ```

## Output Files

The tool generates country-specific prefix files:

**Iranian prefixes:**
- `ir_prefixes_v4.txt` - IPv4 prefixes as /24 blocks
- `ir_prefixes_v6.txt` - IPv6 prefixes

**Chinese prefixes:**
- `cn_prefixes_v4.txt` - IPv4 prefixes as /24 blocks  
- `cn_prefixes_v6.txt` - IPv6 prefixes

**Russian prefixes:**
- `ru_prefixes_v4.txt` - IPv4 prefixes as /24 blocks
- `ru_prefixes_v6.txt` - IPv6 prefixes

## How It Works

1. **Fetches ASN Lists**: Downloads the latest ASN allocations from all RIRs for comprehensive coverage:
   - **Iran**: Primary coverage from RIPE NCC, with additional checks across all RIRs
   - **China**: Primary coverage from APNIC, with additional checks across all RIRs
   - **Russia**: Primary coverage from RIPE NCC, with additional checks across all RIRs
2. **Downloads BGP Data**: Fetches the complete BGP routing table from bgp.tools
3. **Filters by ASN**: Keeps only prefixes from the dynamically fetched ASN lists
4. **Processes IPv4**: Converts IPv4 prefixes to /24 blocks for consistency
5. **Processes IPv6**: Aggregates IPv6 prefixes into the minimal set with identical coverage (no IPs added or removed)
6. **Sorts and Saves**: Outputs clean, sorted prefix lists to text files

## Data Sources

- **RIR Delegated Files**: 
  - Iran: [RIPE NCC](https://ftp.ripe.net/ripe/stats/delegated-ripencc-latest) (+ all other RIRs)
  - China: [APNIC](https://ftp.apnic.net/stats/apnic/delegated-apnic-latest) (+ all other RIRs)
  - Russia: [RIPE NCC](https://ftp.ripe.net/ripe/stats/delegated-ripencc-latest) (+ all other RIRs)
- **BGP Data**: [bgp.tools](https://bgp.tools/table.jsonl)

## License

This project is licensed under the MIT License.

## Contributions

Contributions are welcome! Feel free to fork the repository and submit a pull request.
