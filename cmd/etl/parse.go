package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Satellite names as they appear in hotspots.csv.gz (contract 1).
var satelliteNames = map[string]string{
	"Terra": "Terra", "T": "Terra",
	"Aqua": "Aqua", "A": "Aqua",
	"N": "SNPP", "SNPP": "SNPP",
	"N20": "NOAA20", "1": "NOAA20", "J1": "NOAA20",
	"N21": "NOAA21", "2": "NOAA21", "J2": "NOAA21",
}

// hotspot is one FIRMS detection, normalized to contract 1.
type hotspot struct {
	date       string // YYYY-MM-DD, UTC
	time       uint16 // HHMM, UTC
	lat, lon   float64
	latS, lonS string // as written by FIRMS, so no digits are lost or invented
	instrument string // MODIS | VIIRS
	satellite  string // Terra | Aqua | SNPP | NOAA20 | NOAA21
	conf       byte   // l | n | h
	confRaw    string
	frp        string
	dayNight   string
	typ        string // "" for NRT
	nrt        bool
	cell       int32
}

const header = "latitude,"

// cellID maps a point to the 0.1° grid cell of contract 1.
func cellID(lat, lon float64) int32 {
	cx := math.Floor((lon+180)*10 + 1e-9)
	cy := math.Floor((lat+90)*10 + 1e-9)
	return int32(cy*3600 + cx)
}

// normConf maps FIRMS confidence to l/n/h: MODIS reports 0–100, VIIRS a letter.
func normConf(instrument, raw string) (byte, error) {
	if instrument == "MODIS" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			return 0, fmt.Errorf("MODIS confidence %q", raw)
		}
		switch {
		case v < 30:
			return 'l', nil
		case v < 80:
			return 'n', nil
		}
		return 'h', nil
	}
	if raw != "" {
		switch c := raw[0] | 0x20; c { // lower-case
		case 'l', 'n', 'h':
			return c, nil
		}
	}
	return 0, fmt.Errorf("VIIRS confidence %q", raw)
}

// parseCSV calls emit for every data row of a FIRMS CSV.
//
// Some FIRMS API responses are two responses glued together without a line
// break, so a header can start in the middle of a line. Every header found,
// wherever it is, restarts the column mapping.
func parseCSV(data string, emit func(hotspot)) error {
	data = strings.ReplaceAll(data, "\r", "")
	data = strings.ReplaceAll(data, header, "\n"+header)

	var col map[string]int
	need := []string{"latitude", "longitude", "acq_date", "acq_time", "satellite",
		"instrument", "confidence", "version", "frp", "daynight"}
	lineNo := 0
	for line := range strings.SplitSeq(data, "\n") {
		if line == "" {
			continue
		}
		lineNo++
		f := strings.Split(line, ",")
		if strings.HasPrefix(line, header) {
			col = make(map[string]int, len(f))
			for i, name := range f {
				col[name] = i
			}
			for _, name := range need {
				if _, ok := col[name]; !ok {
					return fmt.Errorf("header has no column %q", name)
				}
			}
			continue
		}
		if col == nil {
			return fmt.Errorf("row %d: data before header", lineNo)
		}
		h, err := parseRow(f, col)
		if err != nil {
			return fmt.Errorf("row %d (%s): %w", lineNo, line, err)
		}
		emit(h)
	}
	return nil
}

func parseRow(f []string, col map[string]int) (hotspot, error) {
	get := func(name string) string {
		i, ok := col[name]
		if !ok || i >= len(f) {
			return ""
		}
		return strings.TrimSpace(f[i])
	}
	var (
		h   hotspot
		err error
	)
	h.latS, h.lonS = get("latitude"), get("longitude")
	if h.lat, err = strconv.ParseFloat(h.latS, 64); err != nil {
		return h, fmt.Errorf("latitude %q", h.latS)
	}
	if h.lon, err = strconv.ParseFloat(h.lonS, 64); err != nil {
		return h, fmt.Errorf("longitude %q", h.lonS)
	}
	h.date = get("acq_date")
	if len(h.date) != 10 || h.date[4] != '-' || h.date[7] != '-' {
		return h, fmt.Errorf("acq_date %q", h.date)
	}
	t, err := strconv.ParseUint(get("acq_time"), 10, 16)
	if err != nil || t > 2359 {
		return h, fmt.Errorf("acq_time %q", get("acq_time"))
	}
	h.time = uint16(t)
	h.instrument = get("instrument")
	if h.instrument != "MODIS" && h.instrument != "VIIRS" {
		return h, fmt.Errorf("instrument %q", h.instrument)
	}
	sat, ok := satelliteNames[get("satellite")]
	if !ok {
		return h, fmt.Errorf("satellite %q", get("satellite"))
	}
	h.satellite = sat
	h.confRaw = get("confidence")
	if h.conf, err = normConf(h.instrument, h.confRaw); err != nil {
		return h, err
	}
	h.frp = get("frp")
	h.dayNight = get("daynight")
	h.typ = get("type")
	h.nrt = strings.HasSuffix(get("version"), "RT") // NRT, and URT/RT if they ever show up
	h.cell = cellID(h.lat, h.lon)
	return h, nil
}
