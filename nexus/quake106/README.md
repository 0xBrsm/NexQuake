# Quake 1.06 Extraction

Standalone Go package that extracts `pak0.pak` directly from the original id Software Quake 1.06 shareware distribution (`quake106.zip`).

## What It Does

The original shareware archive uses a multi-part LHA-compressed installer format from 1996. This package implements LZH (LH5) decompression from scratch in pure Go (no cgo, no external binaries, no shell calls) to decompress the `resource.1` segment and extract the PAK file and license text.

Every step is SHA256-verified: the zip file itself, the `resource.1` entry, and the extracted `pak0.pak`. If any hash does not match the known-good value, extraction fails.

## Why Verification Matters

Hash verification serves two purposes:

1. **Correctness.** The engine expects specific file layouts. A corrupted or modified PAK causes subtle gameplay issues or crashes.
2. **License compliance.** id Software's shareware license permits redistribution of the original, unmodified archive only. The extraction pipeline enforces this by rejecting anything that doesn't match known-good hashes.

## How It Fits

This is what makes Nexus's auto-bootstrap work. On first run, if no game data exists, Nexus downloads `quake106.zip`, hands it to this package, and gets a verified `pak0.pak` out the other side. The bootstrap process is described in the [quickstart manifests documentation](../../manifests/README.md).

## LZH Lineage

The LZH decoding logic is derived from [koron-go/lha](https://github.com/koron-go/lha) (MIT licensed), heavily modified and optimized for Quake 1.06 resource extraction. See [ATTRIBUTIONS.md](../../ATTRIBUTIONS.md) for full provenance.

## Shareware Sources

- https://github.com/Jason2Brownlee/QuakeOfficialArchive/blob/main/bin/quake106.zip
- https://www.gamers.org/pub/idgames/idstuff/quake/quake106.zip
