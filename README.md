# PMF2BIN

**PMF2BIN** converts Kodak PhotoCD *Premaster Files* (`.pmf` / `.pmf.ff`) into standard **BIN/CUE** disc images.
Unlike other tools, which often produce invalid sector layouts, truncated tracks, or corrupted audio, PMF2BIN accurately parses the `.pmf.ff` metadata and constructs each sector correctly.
This allows you to burn fully functional PhotoCD Portfolio discs with **any CD burner** and **any media**, without requiring proprietary Kodak hardware or media.

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

### Build from Source

```
git clone https://github.com/yourusername/pmf2bin.git
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

If you prefer to split the combined BIN file into separate track images, you can use **binmerge**:  
👉 https://github.com/putnam/binmerge

Example:

```
binmerge -split file.bin
```

---

## Technical Details

For a deeper look at how **PMF2BIN** performs its conversions, this section describes the internal logic and data handling that make it possible to construct a valid **BIN/CUE** image from Kodak PhotoCD *Premaster* files.  
Rather than just repackaging data, PMF2BIN parses the `.pmf.ff` metadata, validates track layout and sector counts, and builds each sector to match the 2352-byte format expected by CD imaging tools.

---

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
  - Correct PMF file size based on total sectors

---

### Sector Conversion

- Kodak PMF sectors are **2056 bytes**, while CD sectors in a BIN image are **2352 bytes**.
- PMF2BIN constructs each full sector by:
  1. Adding the **12-byte sync header** (`00 FF FF … 00`)
  2. Adding a **4-byte MSF header** with minute/second/frame position
  3. Inserting the **8-byte subheader** from PMF data
  4. Writing the **2048 bytes of user data**
  5. Zero-filling EDC/ECC areas (unused by Kodak's premaster format)

- Audio tracks (Mode 4) are written as raw **16-bit stereo PCM** sectors (2352 bytes per sector).  
  If the FF file specifies `AUDIO_MSB`, PMF2BIN swaps bytes per sample to match endianness.

---

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

### Validation

Before writing output, PMF2BIN ensures:
- PMF file length matches total sectors from the FF definition.
- Tracks are correctly ordered and contiguous.
- Modes are valid (`2` or `4` only).
- No negative pregaps or overlapping sectors.

---

## Acknowledgments

- **edcre** – [https://github.com/alex-free/edcre](https://github.com/alex-free/edcre)  
  for invaluable help in validating PMF2BIN's output.

---

## License

This project is licensed under the **GNU General Public License v3.0 (GPL-3.0)**.  
See the [LICENSE](LICENSE) file for details.
