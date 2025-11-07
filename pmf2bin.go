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

func parseFF(ffPath string, pmfLen int) ([]Track, error) {
	f, err := os.Open(ffPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %v", ffPath, err)
	}
	defer f.Close()

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

func buildBin(pmf []byte, tracks []Track, outPath string) error {
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("Failed to create %s: %v", outPath, err)
	}
	defer out.Close()
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
			// EDC and ECC remain zeros
			offset = end
			bw.Write(sector[:])
		}
	}

	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush failed: %v", err)
	}

	fmt.Printf("Wrote BIN image: %s\n", outPath)

	if offset != len(pmf) {
		return fmt.Errorf("PMF file not fully consumed: %d bytes remaining", len(pmf)-offset)
	}
	return nil
}

func writeCue(tracks []Track, cuePath, binName string) error {
	f, err := os.Create(cuePath)
	if err != nil {
		return fmt.Errorf("Failed to write cue: %v", err)
	}
	defer f.Close()

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
