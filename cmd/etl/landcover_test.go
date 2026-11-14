package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// bigGeoTIFF builds an uncompressed 8-bit BigTIFF with tw×th tiles, its
// top-left corner at (lon0, lat0) and pixels px degrees wide.
func bigGeoTIFF(w, h, tw, th int, pix []byte, lon0, lat0, px float64) []byte {
	le := binary.LittleEndian
	across, down := (w+tw-1)/tw, (h+th-1)/th
	nTiles := across * down

	// Blobs referenced from the directory, laid out right after it.
	var blob bytes.Buffer
	const nEntries = 11
	base := uint64(16 + 8 + nEntries*20 + 8)
	put := func(v any) uint64 {
		off := base + uint64(blob.Len())
		binary.Write(&blob, le, v)
		return off
	}
	tileOffsets := make([]uint64, nTiles)
	tileSizes := make([]uint64, nTiles)
	for i := range nTiles {
		tile := make([]byte, tw*th) // padded with zeros at the edges
		x0, y0 := i%across*tw, i/across*th
		for y := y0; y < min(y0+th, h); y++ {
			copy(tile[(y-y0)*tw:], pix[y*w+x0:y*w+min(x0+tw, w)])
		}
		tileOffsets[i], tileSizes[i] = put(tile), uint64(len(tile))
	}
	offsetsAt, sizesAt := put(tileOffsets), put(tileSizes)
	scaleAt := put([]float64{px, px, 0})
	tieAt := put([]float64{0, 0, 0, lon0, lat0, 0})

	type entry struct {
		Tag, Type    uint16
		Count, Value uint64
	}
	entries := []entry{
		{tagWidth, 16, 1, uint64(w)}, {tagHeight, 16, 1, uint64(h)},
		{tagBits, 16, 1, 8}, {tagCompression, 16, 1, 1}, {tagSamples, 16, 1, 1},
		{tagTileWidth, 16, 1, uint64(tw)}, {tagTileHeight, 16, 1, uint64(th)},
		{tagTileOffsets, 16, uint64(nTiles), offsetsAt}, {tagTileSizes, 16, uint64(nTiles), sizesAt},
		{tagPixelScale, 12, 3, scaleAt}, {tagTiepoint, 12, 6, tieAt},
	}
	var b bytes.Buffer
	b.WriteString("II")
	binary.Write(&b, le, []uint16{43, 8, 0})
	binary.Write(&b, le, uint64(16))
	binary.Write(&b, le, uint64(nEntries))
	binary.Write(&b, le, entries)
	binary.Write(&b, le, uint64(0))
	b.Write(blob.Bytes())
	return b.Bytes()
}

func TestLandCover(t *testing.T) {
	dir := t.TempDir()
	// 3×3 pixels of 0.5° in 2×2 tiles, top-left corner at lon 30, lat 50.
	write := func(name string, pix []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), bigGeoTIFF(3, 3, 2, 2, pix, 30, 50, 0.5), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("MCD12Q1.061_LC_Type1_doy2001001000000_aid0001.tif", []byte{1, 2, 3, 4, 5, 255, 7, 8, 9})
	write("MCD12Q1.061_LC_Type1_doy2003001000000_aid0001.tif", []byte{12, 12, 12, 10, 10, 10, 14, 14, 14})
	write("MCD12Q1.061_QC_doy2001001000000_aid0001.tif", []byte{0, 0, 0, 0, 0, 0, 0, 0, 0}) // must be ignored

	lc, err := loadLandCover(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		lon, lat float64
		year     int
		want     string
	}{
		{30.1, 49.9, 2001, "1"},  // top-left pixel
		{31.4, 49.9, 2001, "3"},  // top-right, second tile
		{30.6, 49.2, 2001, "5"},  // middle
		{31.4, 49.2, 2001, ""},   // fill value
		{30.1, 48.6, 2001, "7"},  // bottom-left, third tile
		{31.4, 48.6, 2001, "9"},  // bottom-right, fourth tile
		{30.1, 49.9, 2000, "1"},  // before the product starts
		{30.1, 49.9, 2002, "1"},  // gap: closest earlier year
		{30.1, 49.9, 2003, "12"}, // class changed
		{30.1, 49.2, 2026, "10"}, // after the last release
		{29.9, 49.9, 2001, ""},   // west of the raster
		{31.6, 49.9, 2001, ""},   // east
		{30.1, 50.1, 2001, ""},   // north
		{30.1, 48.4, 2001, ""},   // south
	} {
		if got := lc.class(c.lon, c.lat, c.year); got != c.want {
			t.Errorf("class(%v, %v, %d) = %q, want %q", c.lon, c.lat, c.year, got, c.want)
		}
	}
}

func TestLandCoverMissing(t *testing.T) {
	lc, err := loadLandCover(filepath.Join(t.TempDir(), "nope"))
	if err != nil || lc != nil {
		t.Fatalf("got %v, %v; want nil, nil", lc, err)
	}
	if got := lc.class(30, 50, 2020); got != "" {
		t.Errorf("class on nil = %q", got)
	}
}

func TestDecodeGeoTIFFRejectsGarbage(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("II*\x00"), bytes.Repeat([]byte{0xff}, 64)} {
		if _, err := decodeGeoTIFF(data); err == nil {
			t.Errorf("decodeGeoTIFF(%q): want error", data)
		}
	}
}
