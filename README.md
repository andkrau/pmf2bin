# PMF2BIN

**PMF2BIN** converts Kodak PhotoCD *Premaster Files* (`.pmf` / `.pmf.ff`) into standard **BIN/CUE** disc images.
Unlike other tools, which often produce invalid sector layouts, truncated tracks, or corrupted audio, PMF2BIN accurately parses the `.pmf.ff` metadata and constructs each sector correctly.
This allows you to burn fully functional PhotoCD Portfolio discs with **any CD burner** and **any media**, without requiring proprietary Kodak hardware or media.

[https://github.com/andkrau/pmf2bin](https://github.com/andkrau/pmf2bin)

---

## Features

- Converts `.pmf` and `.pmf.ff` Premaster files into standard BIN/CUE images.
- Supports **PhotoCD Portfolio** disc structures.
- Validates track data and PMF/FF file integrity.
- Automatically handles **AUDIO** and **MODE2** tracks.
- Cross-platform: works on **Windows**, **Linux**, and **macOS**.
- Windows version opens a file picker dialog if no arguments are given.

---

## Installation

No installation is required — just build from source or download a compiled binary (if available).

The 32-bit build supports Windows XP and above. XP requires SP3, .NET Framework, and PowerShell. Vista requires PowerShell. These dependencies are only required if not running the application via the command line.

The 64-bit build supports Windows 7 and above.

### Build from Source

```
git clone https://github.com/andkrau/pmf2bin.git
cd pmf2bin
go build
```

This produces an executable named `pmf2bin` (or `pmf2bin.exe` on Windows).

---

## Usage

### Windows

If you run `pmf2bin.exe` without arguments, a dialog will open allowing you to select your `.pmf` or `.pmf.ff` file.

Alternatively, you can use the command line:

```
pmf2bin file.pmf
# or
pmf2bin file.pmf.ff
```

### Linux / macOS

Run PMF2BIN directly from the terminal:

```
./pmf2bin file.pmf.ff
```

The program will generate two output files in the same directory:

```
file.bin
file.cue
```

These can then be burned to CD using any standard CD writing tool.

---

## Multiple BIN Files

If you prefer to split the combined BIN file into separate track images, you can use **binmerge**: https://github.com/putnam/binmerge

Example:

```
binmerge --split --outdir "./newfolder" "file.cue" "newfile"
```

---

## Technical Details

For a deeper look at how **PMF2BIN** performs its conversions, this section describes the internal logic and data handling that make it possible to construct a valid **BIN/CUE** image from Kodak PhotoCD *Premaster* files.
Rather than just repackaging data, PMF2BIN parses the `.pmf.ff` metadata, validates track layout and sector counts, and builds each sector to match the 2352-byte format expected by CD imaging tools.

### Track and Sector Parsing

- The `.pmf.ff` file lists track metadata lines like:
  ```
  1 2 0 129599
  2 4 129600 160199
  ```
  Each line specifies:
  - **Track number**
  - **Mode** (`2` for Mode 2 / Form 1 data, `4` for audio)
  - **Start sector**
  - **End sector**

- PMF2BIN reads these entries, validates them, and checks for:
  - Sequential numbering
  - No overlapping tracks
  - Modes are valid (`2` or `4`)
  - Pregaps are non-negative
  - PMF file length matches the sum of all track sectors

### Sector Conversion

- Kodak PMF sectors are **2056 bytes**, while CD sectors in a BIN image are **2352 bytes**.
- PMF2BIN constructs each full sector by:
  1. Adding the **12-byte sync header** (`00 FF FF … 00`)
  2. Adding a **4-byte MSF header** with minute/second/frame position
  3. Inserting the **8-byte subheader** from PMF data
  4. Writing the **2048 bytes of user data**
  5. Computing and writing the **4-byte EDC** (Error Detection Code)
  6. Computing and writing the **172-byte P-parity ECC** (Error Correction Code)
  7. Computing and writing the **104-byte Q-parity ECC** (Error Correction Code)

- Audio tracks (Mode 4) are written as raw **16-bit stereo PCM** sectors (2352 bytes per sector).
  If the FF file specifies `AUDIO_MSB`, PMF2BIN swaps bytes per sample to match endianness.

### Error Detection Code (EDC)
- PMF2BIN calculates a **32-bit EDC checksum** for each CD-ROM XA Mode 2 Form 1 sector.
- The EDC is computed over **2072 bytes** (from the sync header through user data, bytes 0-2071).
- Uses a **reflected CRC-32** algorithm with polynomial **0xD8018001** (reflection of 0x04C11DB7).
- Unlike standard CRC-32, **no initial or final XOR** is applied (initial value is 0x00000000).
- The 4-byte EDC is stored in **little-endian** byte order at bytes 2072-2075.
- This checksum allows CD-ROM drives to detect data corruption in the user data area.

### Error Correction Code (ECC)

CD-ROM Mode 2 Form 1 sectors use **Reed-Solomon Product Code (RSPC)** for error correction, as specified in ECMA-130 Annex A. This consists of **276 bytes of ECC**, which are calculated as two separate parity blocks: **P-parity** and **Q-parity**. PMF2BIN computes these using **2-stage Linear Feedback Shift Registers (LFSRs)**.

#### LFSR Overview

The LFSR method simulates a shift register where data flows through two stages with feedback, mathematically equivalent to dividing the data polynomial by the generator polynomial and keeping the remainder as parity.

- **Generator polynomial:** `g(x) = x² + g₁x + g₀` with roots at α⁰ and α¹ (where `α = 2` is the primitive element of the field)
- **Field:** GF(2⁸) with the standard CD-ROM field polynomial `x⁸ + x⁴ + x³ + x² + 1` (0x11D)
- **Feedback coefficients:** `g₀ = 2` and `g₁ = 3`

After processing all data through the registers, r0 and r1 contain the two parity bytes needed for error correction.

#### P-Parity (Columns)

- Covers **43 columns × 24 rows** of interleaved sector data.
- Processed columns sequentially, row by row within each column using the LFSR.
- **Input:**  2064 bytes (header + subheader + data + EDC, header bytes treated as 0)
- **Output:** 172 bytes
  - Bytes 0–85: `r1` values for all 43 columns (LSB/MSB pairs)
  - Bytes 86–171: `r0` values for all 43 columns (LSB/MSB pairs)

#### Q-Parity (Diagonals)

- Covers **26 diagonals × 43 elements** of interleaved sector data.
- Diagonals wrap around at byte 2236, following an 88-byte stride pattern.
- **Input:**  2236 bytes (header + subheader + data + EDC + P-parity, header bytes treated as 0)
- **Output:** 104 bytes
  - Bytes 0–51: `r1` values for all 26 diagonals (LSB/MSB pairs)
  - Bytes 52–103: `r0` values for all 26 diagonals (LSB/MSB pairs)

Together, P-parity and Q-parity allow the CD-ROM drive to **detect and correct errors** in user data across the sector.

### Pregaps and CUE Sheet

- Pregap lengths are automatically calculated from gaps between tracks in the `.pmf.ff` data.
- The `.cue` file is generated alongside the `.bin` with proper `TRACK`, `INDEX 00`, and `INDEX 01` entries:

  ```
  FILE "file.bin" BINARY
    TRACK 01 MODE2/2352
      INDEX 01 00:00:00
    TRACK 02 AUDIO
      INDEX 00 28:50:00
      INDEX 01 28:52:00
  ```

---

## Acknowledgments

- **edcre** – [https://github.com/alex-free/edcre](https://github.com/alex-free/edcre)
  for invaluable help in validating PMF2BIN's output.

---

## License

This project is licensed under the **GNU General Public License v3.0 (GPL-3.0)**.
See the [LICENSE](LICENSE) file for details.
