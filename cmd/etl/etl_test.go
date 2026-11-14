package main

import "testing"

func TestCellID(t *testing.T) {
	// Examples from contract 1.
	for _, c := range []struct {
		lat, lon float64
		want     int32
	}{
		{47.08959, 37.612, 4934176},
		{45.25808, 31.68034, 4869316},
		{50.3, 30.5, 1403*3600 + 2105}, // exactly on a cell edge
	} {
		if got := cellID(c.lat, c.lon); got != c.want {
			t.Errorf("cellID(%v, %v) = %d, want %d", c.lat, c.lon, got, c.want)
		}
	}
}

func TestNormConf(t *testing.T) {
	for _, c := range []struct {
		instrument, raw string
		want            byte
	}{
		{"MODIS", "0", 'l'}, {"MODIS", "29", 'l'}, {"MODIS", "30", 'n'},
		{"MODIS", "79", 'n'}, {"MODIS", "80", 'h'}, {"MODIS", "100", 'h'},
		{"VIIRS", "l", 'l'}, {"VIIRS", "n", 'n'}, {"VIIRS", "h", 'h'}, {"VIIRS", "H", 'h'},
	} {
		got, err := normConf(c.instrument, c.raw)
		if err != nil || got != c.want {
			t.Errorf("normConf(%s, %q) = %c, %v; want %c", c.instrument, c.raw, got, err, c.want)
		}
	}
	if _, err := normConf("VIIRS", "x"); err == nil {
		t.Error("normConf(VIIRS, x): want error")
	}
}

const (
	spHeader  = "latitude,longitude,bright_ti4,scan,track,acq_date,acq_time,satellite,instrument,confidence,version,bright_ti5,frp,daynight,type"
	nrtHeader = "latitude,longitude,brightness,scan,track,acq_date,acq_time,satellite,instrument,confidence,version,bright_t31,frp,daynight"
)

func parseAll(t *testing.T, data string) []hotspot {
	t.Helper()
	var hs []hotspot
	if err := parseCSV(data, func(h hotspot) { hs = append(hs, h) }); err != nil {
		t.Fatal(err)
	}
	return hs
}

func TestParseCSV(t *testing.T) {
	hs := parseAll(t, spHeader+"\r\n"+
		"47.08959,37.612,333.1,0.53,0.42,2015-01-01,931,N,VIIRS,n,2,277.08,4.45,D,2\r\n"+
		"48.1,30.6,300.3,1.2,1.1,2020-01-03,2354,N20,VIIRS,h,2,268.4,12.6,N,0\n")
	if len(hs) != 2 {
		t.Fatalf("got %d rows, want 2", len(hs))
	}
	h := hs[0]
	if h.date != "2015-01-01" || h.time != 931 || h.satellite != "SNPP" || h.conf != 'n' ||
		h.frp != "4.45" || h.typ != "2" || h.nrt || h.cell != 4934176 || h.latS != "47.08959" {
		t.Errorf("row 0 = %+v", h)
	}
	if hs[1].satellite != "NOAA20" {
		t.Errorf("row 1 satellite = %s", hs[1].satellite)
	}
}

func TestParseCSVNRTWithoutType(t *testing.T) {
	hs := parseAll(t, nrtHeader+"\n45.25808,31.68034,310,1,1,2026-09-01,219,Aqua,MODIS,53,6.1NRT,271,8.36,N\n")
	if len(hs) != 1 || !hs[0].nrt || hs[0].typ != "" || hs[0].conf != 'n' || hs[0].confRaw != "53" {
		t.Errorf("got %+v", hs)
	}
}

// FIRMS sometimes glues two responses together with no line break, and the
// two may even have different columns.
func TestParseCSVGluedHeaders(t *testing.T) {
	hs := parseAll(t, spHeader+nrtHeader+"\n45.25808,31.68034,310,1,1,2026-09-01,219,Aqua,MODIS,53,6.1NRT,271,8.36,N\n")
	if len(hs) != 1 || hs[0].satellite != "Aqua" {
		t.Errorf("got %+v", hs)
	}
	if hs := parseAll(t, spHeader); len(hs) != 0 { // header only, no newline
		t.Errorf("got %+v", hs)
	}
}

func TestParseCSVBadRow(t *testing.T) {
	err := parseCSV(spHeader+"\n1,2,3,4,5,2020-01-01,1200,X9,VIIRS,n,2,1,1,D,0\n", func(hotspot) {})
	if err == nil {
		t.Error("unknown satellite: want error")
	}
}

func TestBorderContains(t *testing.T) {
	// A 10×10 square with a 2×2 hole in the middle.
	b := newBorder([][][]float64{
		{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {0, 0}},
		{{4, 4}, {6, 4}, {6, 6}, {4, 6}, {4, 4}},
	})
	for _, c := range []struct {
		lon, lat float64
		want     bool
	}{
		{1, 1, true}, {9.9, 9.9, true}, {5, 5, false}, {5, 3.9, true},
		{-1, 5, false}, {11, 5, false}, {5, -1, false}, {5, 11, false},
	} {
		if got := b.contains(c.lon, c.lat); got != c.want {
			t.Errorf("contains(%v, %v) = %v, want %v", c.lon, c.lat, got, c.want)
		}
	}
}

func TestDedupe(t *testing.T) {
	sp := hotspot{date: "2026-08-31", time: 1000, satellite: "Aqua", lat: 50, lon: 30}
	nrtSameDay := hotspot{date: "2026-08-31", time: 1100, satellite: "Aqua", lat: 50, lon: 30, nrt: true}
	nrtOtherSat := hotspot{date: "2026-08-31", time: 1100, satellite: "Terra", lat: 50, lon: 30, nrt: true}
	nrtNextDay := hotspot{date: "2026-09-01", time: 900, satellite: "Aqua", lat: 50, lon: 30, nrt: true}
	var st stats
	got := dedupe([]hotspot{nrtNextDay, nrtOtherSat, sp, nrtSameDay, sp}, &st)
	if len(got) != 3 || st.nrtOverSP != 1 || st.duplicates != 1 {
		t.Fatalf("got %d rows, stats %+v", len(got), st)
	}
	if got[0] != sp || got[1] != nrtOtherSat || got[2] != nrtNextDay {
		t.Errorf("order: %+v", got)
	}
}
