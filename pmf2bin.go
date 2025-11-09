package main

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

type Track struct {
	Num    int
	Mode   int
	Start  int
	End    int
	Pregap int // number of sectors in pregap (INDEX 00)
}

const (
	pmfSector = 2056
	binSector = 2352
)

var (
	audioMSB bool
	edcLUT [256]uint32
	gfLog [256]byte
	gfPow [509]byte
)

func init() {
	// Create EDC Lookup table
	const polyEDC uint32 = 0xD8018001 // reflected polynomial of 0x04C11DB7
	for i := 0; i < 256; i++ {
		r := uint32(i)
		for j := 0; j < 8; j++ {
			if r&1 != 0 {
				r = (r >> 1) ^ polyEDC
			} else {
				r >>= 1
			}
		}
		edcLUT[i] = r
	}

	// Creates the exponentiation (gfPow) and logarithm (gfLog) tables
	// for the Galois Field GF(2^8), essential for Reed-Solomon arithmetic.
	// The tables are generated using the irreducible polynomial: x^8 + x^4 + x^3 + x^2 + 1,
	// which corresponds to the reduction value 0x11D.
	// The process generates successive powers of the primitive element α (alpha).
	//
	// When b exceeds 8 bits, b ^= 0x11d reduces it by the irreducible polynomial.
	// The second loop extends gfPow to 509 elements to avoid modulo 255
	// operations during multiplication (optimization: gfPow[a+b] instead of gfPow[(a+b)%255]).
	var b uint16 = 1
	for i := 0; i < 255; i++ {
		gfPow[i] = byte(b)
		gfLog[b] = byte(i)
		b <<= 1
		if b&0x100 != 0 {
			b ^= 0x11d
		}
	}
	for i := 255; i < 509; i++ {
		gfPow[i] = gfPow[i-255]
	}

	setConsoleTitle("PMF2BIN")
}

func main() {
	var path string
	defer pauseOnExit()

	if len(os.Args) < 2 {
		if runtime.GOOS == "windows" {
		cmd := exec.Command("powershell", "-Command",
			`Add-Type -AssemblyName System.Windows.Forms;
			$f = New-Object System.Windows.Forms.OpenFileDialog;
			$f.Filter = "Premaster files (*.pmf,*.pmf.ff)|*.pmf;*.pmf.ff";
			if ($f.ShowDialog() -eq 'OK') { Write-Output $f.FileName }`)
		out, err := cmd.Output()
		if err != nil {
			log.Println("No file selected or error: ", err)
			return
		}
		path = strings.TrimSpace(string(out))
		if path == "" {
			log.Println("No file selected!")
			return
		}
		} else {
			fmt.Printf("Usage: %s <file.pmf.ff>", os.Args[0])
			return
		}
	} else {
		path = os.Args[1]
	}

	base := strings.TrimSuffix(strings.TrimSuffix(path, ".ff"), ".pmf")
	pmfPath := base + ".pmf"
	ffPath := base + ".pmf.ff"
	pmf, err := ioutil.ReadFile(pmfPath)
	if err != nil {
		log.Printf("Failed to read %s: %v", pmfPath, err)
		return
	}

	tracks, err := parseFF(ffPath, len(pmf))
	if err != nil {
		log.Printf("Failed to parse/validate %s: %v", ffPath, err)
		return
	}

	outBin := base + ".bin"
	outCue := base + ".cue"

	err = buildBin(pmf, tracks, outBin)
	if err != nil {
		log.Printf("Failed to build bin %s: %v", outBin, err)
		return
	}

	err = writeCue(tracks, outCue, outBin)
	if err != nil {
		log.Printf("Failed to write cue %s: %v", outCue, err)
		return
	}

	fmt.Println("\nDone!")
}

func pauseOnExit() {
	fmt.Println("\nPress Enter to exit...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')
}

func setConsoleTitle(title string) {
	switch runtime.GOOS {
	case "windows":
		// Use CMD method; works in both CMD and PowerShell
		cmd := exec.Command("cmd", "/C", "title", title)
		cmd.Run() // ignore errors for simplicity
	case "linux", "darwin":
		// ANSI escape sequence for most terminals
		fmt.Printf("\033]0;%s\007", title)
	}
}

func parseFF(ffPath string, pmfLen int) ([]Track, err error) {
	f, err := os.Open(ffPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %v", ffPath, err)
	}
	defer func() {
		// Always attempt to close, even if an earlier error occurred
		closeErr := f.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("Close failed: %v", closeErr)
		}
	}()

	scanner := bufio.NewScanner(f)
	var tracks []Track
	var numExpected int
	inSection := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines
		if line == "" {
			continue
		}
		// Detect audio byte order
		if strings.HasPrefix(line, "AUDIO_BYTE_ORDER:") {
			order := strings.TrimSpace(strings.TrimPrefix(line, "AUDIO_BYTE_ORDER:"))
			audioMSB = order == "AUDIO_MSB"
			continue
		}
		// Detect number of tracks
		if strings.HasPrefix(line, "%NUMBER_OF_ADDED_TRACKS") {
			fmt.Sscanf(line, "%%NUMBER_OF_ADDED_TRACKS %d", &numExpected)
			continue
		}
		if strings.HasPrefix(line, "%START_OF_ADDED_TRACK_DATA") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}

		var t Track
		_, err := fmt.Sscanf(line, "%d %d %d %d", &t.Num, &t.Mode, &t.Start, &t.End)
		if err != nil {
			continue // skip malformed line
		}
		tracks = append(tracks, t)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %v", ffPath, err)
	}

	if len(tracks) == 0 {
		return nil, fmt.Errorf("no tracks found in pmf.ff")
	}

	if numExpected > 0 && len(tracks) != numExpected {
		return nil, fmt.Errorf("track count mismatch: expected %d, found %d",
			numExpected, len(tracks))
	}

	// Validate each track
	for i := range tracks {
		t := &tracks[i]

		// Mode check
		if t.Mode != 2 && t.Mode != 4 {
			return nil, fmt.Errorf("track %d has invalid mode %d", t.Num, t.Mode)
		}

		// Sequential numbering check
		if t.Num != i+1 {
			return nil, fmt.Errorf("track numbering mismatch: got %d, expected %d", t.Num, i+1)
		}

		// Logical start/end
		if t.Start > t.End {
			return nil, fmt.Errorf("track %d start sector (%d) is after end sector (%d)", t.Num, t.Start, t.End)
		}

		// Pregap calculation
		if i == 0 {
			t.Pregap = 0
		} else {
			prev := &tracks[i-1]
			t.Pregap = t.Start - prev.End - 1
			if t.Pregap < 0 {
				return nil, fmt.Errorf("track %d has negative pregap (%d sectors)", t.Num, t.Pregap)
			}
			if t.Start <= prev.End {
				return nil, fmt.Errorf("track %d overlaps previous track (start=%d, prev end=%d)", t.Num, t.Start, prev.End)
			}
		}

		// Audio ordering warning
		if i > 0 && tracks[i-1].Mode == 4 && t.Mode != 4 {
			fmt.Printf("Warning: data track follows audio track (unusual ordering)\n")
		}
	}

	// Verify tracks align with PMF size
	expectedSize := 0
	for _, t := range tracks {
		sectorCount := t.End - t.Start + 1 // if End is inclusive
		if t.Mode == 4 {
			expectedSize += sectorCount * binSector
		} else {
			expectedSize += sectorCount * pmfSector
		}
	}
	if expectedSize != pmfLen {
		return nil, fmt.Errorf("PMF length mismatch: expected %d bytes, got %d bytes", expectedSize, pmfLen)
	}

	return tracks, nil
}

func buildBin(pmf []byte, tracks []Track, outPath string) (err error) {
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("Failed to create %s: %v", outPath, err)
	}
	defer func() {
		// Always attempt to close, even if an earlier error occurred
		closeErr := out.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("Close failed: %v", closeErr)
		}
	}()
	bw := bufio.NewWriter(out)
	var sector [binSector]byte
	empty := make([]byte, binSector)
	offset := 0

	for _, t := range tracks {
		trackType := "MODE2"
		if t.Mode == 4 {
			trackType = "AUDIO"
		}
		min, sec, frame := lbaToMSF(t.Start)
		fmt.Printf("Writing Track %d Type %s (%02d:%02d:%02d) Sectors %d–%d\n", t.Num, trackType, min, sec, frame, t.Start, t.End)

		// Write pregap sectors
		for s := 0; s < t.Pregap; s++ {
			lba := t.Start - t.Pregap + s + 150
			min, sec, frame := lbaToMSF(lba)

			copy(sector[:], empty) // zeroes by default

			if t.Mode == 2 {
				// 12-byte sync
				copy(sector[0:12], []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00})
				// 4-byte header with accurate MSF
				sector[12] = toBCD(min)
				sector[13] = toBCD(sec)
				sector[14] = toBCD(frame)
				sector[15] = byte(t.Mode)
				// 8-byte subheader with submode byte signaling Mode 2 Form 1
				//copy(sector[16:24], []byte{0x00, 0x00, 0x20, 0x00, 0x00, 0x00, 0x20, 0x00})
				// 4-byte end of pregap sector on many discs
				//copy(sector[2044:2048], []byte{0x3F, 0x13, 0xB0, 0xBE})
				// Data and ECC remain zeros
			}
			bw.Write(sector[:])
		}

		// Write actual track sectors
		for s := t.Start; s <= t.End; s++ {
			lba := s + 150
			min, sec, frame := lbaToMSF(lba)

			copy(sector[:], empty) // zeros by default

			if t.Mode == 4 {
				end := offset + binSector
				if end > len(pmf) {
					return fmt.Errorf("PMF truncated: need %d bytes, only %d available", end, len(pmf))
				}
				data := pmf[offset:end]
			if audioMSB {
				// Swap every pair of bytes (16-bit samples)
				for i := 0; i+1 < len(data); i += 2 {
					data[i], data[i+1] = data[i+1], data[i]
				}
			}
				bw.Write(data)
				offset = end
				continue
			}

			end := offset + pmfSector
			if end > len(pmf) {
				return fmt.Errorf("PMF truncated: need %d bytes, only %d available", end, len(pmf))
			}
			raw := pmf[offset:end]
			sub := raw[:8]
			data := raw[8:]

			// 12-byte sync
			copy(sector[0:12], []byte{0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00})
			// 4-byte header with accurate MSF
			sector[12] = toBCD(min)
			sector[13] = toBCD(sec)
			sector[14] = toBCD(frame)
			sector[15] = byte(t.Mode)
			// 8-byte subheader from PMF
			copy(sector[16:24], sub)
			// 2048 bytes of data
			copy(sector[24:2072], data)
			// 4-byte calculated EDC
			edc := computeEDC(sector[16:2072])
			copy(sector[2072:2076], edc[:])
			// 172-byte P-parity
			pParity := pParityLFSR(sector[12:2076])
			copy(sector[2076:2248], pParity)
			// 104-byte Q-parity
			qParity := qParityLFSR(sector[12:2248])
			copy(sector[2248:2352], qParity)
			offset = end
			bw.Write(sector[:])
		}
	}

	if err := bw.Flush(); err != nil {
		return fmt.Errorf("Flush failed: %v", err)
	}

	if err := out.Sync(); err != nil {
		return fmt.Errorf("Sync failed: %v", err)
	}

	fmt.Printf("Wrote BIN image: %s\n", outPath)

	if offset != len(pmf) {
		return fmt.Errorf("PMF file not fully consumed: %d bytes remaining", len(pmf)-offset)
	}
	return nil
}

func writeCue(tracks []Track, cuePath, binName string) (err error) {
	f, err := os.Create(cuePath)
	if err != nil {
		return fmt.Errorf("Failed to write cue: %v", err)
	}
	defer func() {
		// Always attempt to close, even if an earlier error occurred
		closeErr := out.Close()
		if err == nil && closeErr != nil {
			err = fmt.Errorf("Close failed: %v", closeErr)
		}
	}()

	fmt.Fprintf(f, "FILE \"%s\" BINARY\n", filepath.Base(binName))
	for _, t := range tracks {
		if t.Mode == 4 {
			fmt.Fprintf(f, "  TRACK %02d AUDIO\n", t.Num)
		} else {
			fmt.Fprintf(f, "  TRACK %02d MODE2/2352\n", t.Num)
		}

		if t.Pregap > 0 {
			min, sec, frame := lbaToMSF(t.Start - t.Pregap)
			fmt.Fprintf(f, "    INDEX 00 %02d:%02d:%02d\n", min, sec, frame)
		}
		fmt.Fprintf(f, "    INDEX 01 %s\n", lbaToMSFFormatted(t.Start))
	}
	fmt.Printf("Wrote CUE sheet: %s\n", cuePath)
	return nil
}

func toBCD(value int) byte {
	return byte((value/10)<<4 | (value%10))
}

func lbaToMSF(lba int) (int, int, int) {
	min := lba / (60 * 75)
	sec := (lba / 75) % 60
	frame := lba % 75
	return min, sec, frame
}

func lbaToMSFFormatted(lba int) string {
	min, sec, frame := lbaToMSF(lba)
	return fmt.Sprintf("%02d:%02d:%02d", min, sec, frame)
}

// computeEDC calculates the 32-bit EDC (Error Detection Code) for a CD-ROM XA Mode 2 Form 1 sector.
// It uses a reflected CRC-32 with polynomial 0x04C11DB7 (reflected as 0xD8018001).
// The EDC covers 2072 bytes from sync header through user data.
// Unlike standard CRC-32, no initial or final XOR is applied.
func computeEDC(data []byte) [4]byte {
	var edc uint32 = 0

	for _, b := range data {
		// Standard reflected CRC-32: XOR byte with accumulator LSB,
		// lookup precomputed value, XOR with shifted accumulator
		index := byte(edc) ^ b
		edc = (edc >> 8) ^ edcLUT[index]
	}

	// Return in little-endian byte order
	return [4]byte{
		byte(edc),
		byte(edc >> 8),
		byte(edc >> 16),
		byte(edc >> 24),
	}
}

// gfMult multiplies two non-zero bytes in GF(2^8) using precomputed logarithm tables.
// Uses the property: a * b = exp(log(a) + log(b)) modulo 255 in GF(2^8).
// Returns 0 if either input is 0.
func gfMult(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfPow[int(gfLog[a])+int(gfLog[b])]
}

// CD-ROM Mode 2 Form 1 P-Parity Generator using a 2-stage LFSR.
//
// Instead of computing the parity formula directly, we simulate a shift register
// that processes sector data sequentially. The feedback taps correspond to the
// generator polynomial coefficients.
//
// For P-parity (2 parity bytes), the generator polynomial is:
//   g(x) = x² + g₁x + g₀
// over GF(2⁸) with the standard CD-ROM field polynomial 0x11d.
// Feedback coefficients for the LFSR are g₀ = 2 and g₁ = 3.
//
// Input:  2064 bytes (header + subheader + data + EDC, header bytes treated as 0)
// Output: 172 bytes organized as:
//   Bytes 0-85:   r1 values for all 43 columns (LSB, MSB pairs)
//   Bytes 86-171: r0 values for all 43 columns (LSB, MSB pairs)
func pParityLFSR(sector []byte) []byte {
	if len(sector) != 2064 {
		panic(fmt.Sprintf("sector wrong size: need 2064 bytes, got %d", len(sector)))
	}

	parity := make([]byte, 172) // 43 columns × 4 bytes

	// Compute parity for each column using LFSR
	for col := 0; col < 43; col++ {
		const (
			g1 = 3 // Feedback coefficient for r1
			g0 = 2 // Feedback coefficient for r0
		)

		var r0Lsb, r0Msb byte
		var r1Lsb, r1Msb byte

		// Process 24 rows vertically through this column
		pos := 2*col
		for row := 0; row < 24; row++ {
			dataLsb := sector[pos]
			dataMsb := sector[pos+1]

			// Treat header bytes 0-3 as zeros for ECC calculation
			if pos < 4 {
				dataLsb = 0
				if pos < 3 {
					dataMsb = 0
				}
			}

			// LFSR feedback and shift operations
			feedbackLsb := dataLsb ^ r1Lsb
			feedbackMsb := dataMsb ^ r1Msb

			r1Lsb = r0Lsb ^ gfMult(feedbackLsb, g1)
			r1Msb = r0Msb ^ gfMult(feedbackMsb, g1)
			r0Lsb = gfMult(feedbackLsb, g0)
			r0Msb = gfMult(feedbackMsb, g0)

			pos += 86 // Stride to next row (2 bytes × 43 columns)
		}

		parity[col*2]      = r1Lsb
		parity[col*2+1]    = r1Msb
		parity[86+col*2]   = r0Lsb
		parity[86+col*2+1] = r0Msb
	}

	return parity
}

// CD-ROM Mode 2 Form 1 Q-Parity Generator using a 2-stage LFSR.
//
// Instead of computing the parity formula directly, we simulate a shift register
// that processes sector data sequentially along diagonals. The feedback taps
// correspond to the same generator polynomial as P-parity.
//
// For Q-parity (2 parity bytes), the generator polynomial is:
//   g(x) = x² + g₁x + g₀
// over GF(2⁸) with the standard CD-ROM field polynomial 0x11d.
// Feedback coefficients for the LFSR are g₀ = 2 and g₁ = 3.
//
// Q-parity covers 26 diagonals × 43 elements of interleaved sector data.
// Diagonals wrap around at byte 2236, following an 88-byte stride pattern.
//
// Input:  2236 bytes (header + subheader + data + EDC + P-parity, header bytes treated as 0)
// Output: 104 bytes organized as follows:
//   Bytes 0-51:   r1 values for all 26 diagonals (LSB/MSB pairs)
//   Bytes 52-103: r0 values for all 26 diagonals (LSB/MSB pairs)
func qParityLFSR(sector []byte) []byte {
	if len(sector) != 2236 {
		panic(fmt.Sprintf("sector wrong size: need 2236 bytes, got %d", len(sector)))
	}

	parity := make([]byte, 104) // 26 diagonals × 4 bytes

	for diag := 0; diag < 26; diag++ {
		const (
			g1 = 3 // Feedback coefficient for r1
			g0 = 2 // Feedback coefficient for r0
		)

		var r0Lsb, r0Msb byte
		var r1Lsb, r1Msb byte

		pos := 2 * 43 * diag  // Start of diagonal

		for step := 0; step < 43; step++ {
			// Wrap diagonal at sector boundary
			if pos >= 2236 {
				pos -= 2236
			}

			dataLsb := sector[pos]
			dataMsb := sector[pos+1]

			// Treat header bytes 0-3 as zeros for ECC calculation
			if pos < 4 {
				dataLsb = 0
				if pos < 3 {
					dataMsb = 0
				}
			}

			// LFSR feedback and shift operations
			feedbackLsb := dataLsb ^ r1Lsb
			feedbackMsb := dataMsb ^ r1Msb

			r1Lsb = r0Lsb ^ gfMult(feedbackLsb, g1)
			r1Msb = r0Msb ^ gfMult(feedbackMsb, g1)
			r0Lsb = gfMult(feedbackLsb, g0)
			r0Msb = gfMult(feedbackMsb, g0)

			pos += 88 // Diagonal stride (2 bytes × 44 positions)
		}

		parity[diag*2]      = r1Lsb
		parity[diag*2+1]    = r1Msb
		parity[52+diag*2]   = r0Lsb
		parity[52+diag*2+1] = r0Msb
	}

	return parity
}
