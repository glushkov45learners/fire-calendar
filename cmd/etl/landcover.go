package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"golang.org/x/image/tiff/lzw"
)

// landCover gives the IGBP land cover class (MCD12Q1 LC_Type1, 1–17) of a
// point in a given year. It is built from the yearly GeoTIFFs in geographic
// coordinates that AppEEARS exports.
type landCover struct {
	years  []int // sorted
	byYear map[int]*raster
}

// raster is a north-up 8-bit grid in lon/lat.
type raster struct {
	lon0, lat0 float64 // outer corner of the top-left pixel
	dx, dy     float64 // pixel size in degrees, both positive
	w, h       int
	pix        []uint8
}

// AppEEARS names the files MCD12Q1.061_LC_Type1_doy2020001000000_aid0001.tif
// and puts quality layers (..._QC_doy...) next to them.
var lcFileName = regexp.MustCompile(`LC_Type1_doy(\d{4})\d{3}`)

// loadLandCover reads every LC_Type1 GeoTIFF in dir. A missing or empty
// directory gives a nil *landCover, whose class method returns "" everywhere.
func loadLandCover(dir string) (*landCover, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.tif"))
	if err != nil {
		return nil, err
	}
	lc := &landCover{byYear: make(map[int]*raster)}
	for _, p := range paths {
		m := lcFileName.FindStringSubmatch(filepath.Base(p))
		if m == nil {
			continue
		}
		year, _ := strconv.Atoi(m[1])
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if lc.byYear[year], err = decodeGeoTIFF(data); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	if len(lc.byYear) == 0 {
		return nil, nil
	}
	lc.years = slices.Sorted(maps.Keys(lc.byYear))
	return lc, nil
}

// class returns the IGBP class as text, or "" when it is unknown. Years the
// product does not have take the closest earlier year, or the first one: 2000
// uses 2001, and years after the last release use that release.
func (lc *landCover) class(lon, lat float64, year int) string {
	if lc == nil {
		return ""
	}
	i, found := slices.BinarySearch(lc.years, year)
	if !found {
		i = max(i-1, 0)
	}
	v, ok := lc.byYear[lc.years[i]].at(lon, lat)
	if !ok || v < 1 || v > 17 { // 255 is the fill value
		return ""
	}
	return strconv.Itoa(int(v))
}

func (r *raster) at(lon, lat float64) (uint8, bool) {
	col := int(math.Floor((lon - r.lon0) / r.dx))
	row := int(math.Floor((r.lat0 - lat) / r.dy))
	if col < 0 || col >= r.w || row < 0 || row >= r.h {
		return 0, false
	}
	return r.pix[row*r.w+col], true
}

// TIFF tags used below.
const (
	tagWidth       = 256
	tagHeight      = 257
	tagBits        = 258
	tagCompression = 259
	tagSamples     = 277
	tagPredictor   = 317
	tagTileWidth   = 322
	tagTileHeight  = 323
	tagTileOffsets = 324
	tagTileSizes   = 325
	tagPixelScale  = 33550
	tagTiepoint    = 33922
)

// decodeGeoTIFF reads the full-resolution image of a BigTIFF as AppEEARS
// writes it: one 8-bit band in tiles, LZW-compressed or raw. The standard
// image decoders do not read BigTIFF, hence this reader.
func decodeGeoTIFF(data []byte) (*raster, error) {
	t, err := readBigTIFF(data)
	if err != nil {
		return nil, err
	}
	one := func(tag uint16, def uint64) uint64 {
		if v := t.ints(tag); len(v) > 0 {
			return v[0]
		}
		return def
	}
	w, h := int(one(tagWidth, 0)), int(one(tagHeight, 0))
	tw, th := int(one(tagTileWidth, 0)), int(one(tagTileHeight, 0))
	compression := one(tagCompression, 1)
	offsets, sizes := t.ints(tagTileOffsets), t.ints(tagTileSizes)
	scale, tie := t.doubles(tagPixelScale), t.doubles(tagTiepoint)
	switch {
	case w <= 0 || h <= 0 || tw <= 0 || th <= 0:
		return nil, errors.New("not a tiled image")
	case one(tagBits, 1) != 8 || one(tagSamples, 1) != 1:
		return nil, errors.New("want one 8-bit band")
	case compression != 1 && compression != 5:
		return nil, fmt.Errorf("unsupported compression %d", compression)
	case one(tagPredictor, 1) != 1:
		return nil, errors.New("unsupported predictor")
	case len(scale) < 2 || len(tie) < 6 || scale[0] <= 0 || scale[1] <= 0:
		return nil, errors.New("no georeferencing")
	}
	across, down := (w+tw-1)/tw, (h+th-1)/th
	if len(offsets) < across*down || len(sizes) < across*down {
		return nil, errors.New("tile table is too short")
	}

	r := &raster{
		// The tiepoint maps raster point (i, j) to (x, y); move it to (0, 0).
		lon0: tie[3] - tie[0]*scale[0],
		lat0: tie[4] + tie[1]*scale[1],
		dx:   scale[0],
		dy:   scale[1],
		w:    w,
		h:    h,
		pix:  make([]uint8, w*h),
	}
	tile := make([]byte, tw*th)
	for i := range across * down {
		off, size := offsets[i], sizes[i]
		if off+size > uint64(len(data)) {
			return nil, errors.New("tile outside the file")
		}
		raw := data[off : off+size]
		if compression == 5 {
			zr := lzw.NewReader(bytes.NewReader(raw), lzw.MSB, 8)
			_, err := io.ReadFull(zr, tile)
			zr.Close()
			if err != nil {
				return nil, fmt.Errorf("tile %d: %w", i, err)
			}
		} else if copy(tile, raw) != len(tile) {
			return nil, fmt.Errorf("tile %d is short", i)
		}
		// Edge tiles are padded to full size; copy only the part inside the image.
		x0, y0 := i%across*tw, i/across*th
		for y := y0; y < min(y0+th, h); y++ {
			copy(r.pix[y*w+x0:y*w+min(x0+tw, w)], tile[(y-y0)*tw:])
		}
	}
	return r, nil
}

// bigTIFF is the first image directory of a BigTIFF file.
type bigTIFF struct {
	bo      binary.ByteOrder
	data    []byte
	entries map[uint16]tiffEntry
}

type tiffEntry struct {
	typ   uint16
	count uint64
	value []byte // count values of the type, in file byte order
}

func readBigTIFF(data []byte) (*bigTIFF, error) {
	bad := errors.New("not a BigTIFF file")
	if len(data) < 16 {
		return nil, bad
	}
	t := &bigTIFF{data: data, entries: make(map[uint16]tiffEntry)}
	switch string(data[:2]) {
	case "II":
		t.bo = binary.LittleEndian
	case "MM":
		t.bo = binary.BigEndian
	default:
		return nil, bad
	}
	if t.bo.Uint16(data[2:]) != 43 || t.bo.Uint16(data[4:]) != 8 {
		return nil, bad
	}
	ifd := t.bo.Uint64(data[8:])
	if ifd+8 > uint64(len(data)) {
		return nil, bad
	}
	n := t.bo.Uint64(data[ifd:])
	if n > 1000 || ifd+8+n*20 > uint64(len(data)) {
		return nil, bad
	}
	typeSize := map[uint16]uint64{1: 1, 2: 1, 3: 2, 4: 4, 12: 8, 16: 8}
	for i := range n {
		e := data[ifd+8+i*20:][:20]
		tag, typ, count := t.bo.Uint16(e), t.bo.Uint16(e[2:]), t.bo.Uint64(e[4:])
		size := typeSize[typ] * count
		if size == 0 {
			continue // a type this reader has no use for
		}
		value := e[12:]
		if size > 8 { // too big to sit in the entry: it holds an offset instead
			off := t.bo.Uint64(e[12:])
			if off+size > uint64(len(data)) {
				return nil, bad
			}
			value = data[off:]
		}
		t.entries[tag] = tiffEntry{typ, count, value[:size]}
	}
	return t, nil
}

// ints returns the values of a SHORT, LONG or LONG8 tag.
func (t *bigTIFF) ints(tag uint16) []uint64 {
	e := t.entries[tag]
	v := make([]uint64, 0, e.count)
	for i := range e.count {
		switch e.typ {
		case 3:
			v = append(v, uint64(t.bo.Uint16(e.value[i*2:])))
		case 4:
			v = append(v, uint64(t.bo.Uint32(e.value[i*4:])))
		case 16:
			v = append(v, t.bo.Uint64(e.value[i*8:]))
		}
	}
	return v
}

// doubles returns the values of a DOUBLE tag.
func (t *bigTIFF) doubles(tag uint16) []float64 {
	e := t.entries[tag]
	if e.typ != 12 {
		return nil
	}
	v := make([]float64, e.count)
	for i := range v {
		v[i] = math.Float64frombits(t.bo.Uint64(e.value[i*8:]))
	}
	return v
}
