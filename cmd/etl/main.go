// Command etl turns raw FIRMS CSV files into hotspots.csv.gz (contract 1).
//
// Input, under -raw:
//
//	firms/<sensor>/*.csv      yearly country archives, already clipped by FIRMS
//	firms_api/<SOURCE>/*.csv  area API chunks for a bounding box, clipped here
//	landcover/*.tif           yearly MCD12Q1 LC_Type1 GeoTIFFs from AppEEARS (optional)
package main

import (
	"bufio"
	"cmp"
	"compress/gzip"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const outHeader = "acq_date,acq_time,lat,lon,instrument,satellite,conf,conf_raw,frp,daynight,type,product,cell_id,lc\n"

type stats struct {
	files, read, outside, nrtOverSP, duplicates, noLandCover int
}

func main() {
	log.SetFlags(0)
	raw := flag.String("raw", "../data/raw", "directory with firms/ and firms_api/")
	borderPath := flag.String("border", "", "country outline GeoJSON (default <raw>/boundaries/geoBoundaries-UKR-ADM0.geojson)")
	lcDir := flag.String("landcover", "", "directory with yearly MCD12Q1 LC_Type1 GeoTIFFs (default <raw>/landcover)")
	out := flag.String("out", "../data/out/hotspots.csv.gz", "output file")
	flag.Parse()
	if *lcDir == "" {
		*lcDir = filepath.Join(*raw, "landcover")
	}
	if *borderPath == "" {
		*borderPath = filepath.Join(*raw, "boundaries", "geoBoundaries-UKR-ADM0.geojson")
	}

	start := time.Now()
	b, err := loadBorder(*borderPath)
	if err != nil {
		log.Fatal(err)
	}
	lc, err := loadLandCover(*lcDir)
	if err != nil {
		log.Fatal(err)
	}
	if lc == nil {
		log.Printf("WARNING: no GeoTIFFs in %s, the lc column will be empty", *lcDir)
	}
	var st stats
	hs, err := readAll(*raw, b, &st)
	if err != nil {
		log.Fatal(err)
	}
	hs = dedupe(hs, &st)
	if err := write(*out, hs, lc, &st); err != nil {
		log.Fatal(err)
	}
	report(hs, st)
	log.Printf("\nwrote %s: %d rows in %s", *out, len(hs), time.Since(start).Round(time.Millisecond))
}

// readAll parses every CSV under raw/firms and raw/firms_api. API chunks cover
// a bounding box, so their points are clipped to the border.
func readAll(raw string, b *border, st *stats) ([]hotspot, error) {
	var hs []hotspot
	for _, sub := range []string{"firms", "firms_api"} {
		clip := sub == "firms_api"
		err := filepath.WalkDir(filepath.Join(raw, sub), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(path) != ".csv" {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			st.files++
			err = parseCSV(string(data), func(h hotspot) {
				st.read++
				if clip && !b.contains(h.lon, h.lat) {
					st.outside++
					return
				}
				hs = append(hs, h)
			})
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return hs, nil
}

// dedupe sorts hotspots and drops NRT rows for every satellite-day that has
// standard (SP) data, then exact repeats of the same detection.
func dedupe(hs []hotspot, st *stats) []hotspot {
	type satDay struct{ satellite, date string }
	hasSP := make(map[satDay]bool)
	for _, h := range hs {
		if !h.nrt {
			hasSP[satDay{h.satellite, h.date}] = true
		}
	}
	hs = slices.DeleteFunc(hs, func(h hotspot) bool {
		drop := h.nrt && hasSP[satDay{h.satellite, h.date}]
		if drop {
			st.nrtOverSP++
		}
		return drop
	})

	slices.SortFunc(hs, func(a, b hotspot) int {
		return cmp.Or(
			strings.Compare(a.date, b.date),
			cmp.Compare(a.time, b.time),
			strings.Compare(a.satellite, b.satellite),
			cmp.Compare(a.lat, b.lat),
			cmp.Compare(a.lon, b.lon),
		)
	})
	n := len(hs)
	hs = slices.CompactFunc(hs, func(a, b hotspot) bool {
		return a.date == b.date && a.time == b.time && a.satellite == b.satellite &&
			a.lat == b.lat && a.lon == b.lon
	})
	st.duplicates = n - len(hs)
	return hs
}

func write(path string, hs []hotspot, lc *landCover, st *stats) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { err = cmp.Or(err, f.Close()) }()
	zw := gzip.NewWriter(f)
	w := bufio.NewWriterSize(zw, 1<<20)

	w.WriteString(outHeader)
	var line []byte
	for _, h := range hs {
		line = append(line[:0], h.date...)
		line = append(line, ',')
		line = fmt.Appendf(line, "%04d", h.time)
		line = append(line, ',')
		line = append(line, h.latS...)
		line = append(line, ',')
		line = append(line, h.lonS...)
		line = append(line, ',')
		line = append(line, h.instrument...)
		line = append(line, ',')
		line = append(line, h.satellite...)
		line = append(line, ',', h.conf, ',')
		line = append(line, h.confRaw...)
		line = append(line, ',')
		line = append(line, h.frp...)
		line = append(line, ',')
		line = append(line, h.dayNight...)
		line = append(line, ',')
		line = append(line, h.typ...)
		if h.nrt {
			line = append(line, ",NRT,"...)
		} else {
			line = append(line, ",SP,"...)
		}
		line = strconv.AppendInt(line, int64(h.cell), 10)
		line = append(line, ',')
		year, _ := strconv.Atoi(h.date[:4])
		class := lc.class(h.lon, h.lat, year)
		if class == "" {
			st.noLandCover++
		}
		line = append(line, class...)
		line = append(line, '\n')
		w.Write(line)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return zw.Close()
}

// report prints a year × satellite table: the quickest way to spot a missing
// year or a broken source.
func report(hs []hotspot, st stats) {
	type key struct{ year, satellite string }
	counts := make(map[key]int)
	years := make(map[string]bool)
	nrt := 0
	for _, h := range hs {
		counts[key{h.date[:4], h.satellite}]++
		years[h.date[:4]] = true
		if h.nrt {
			nrt++
		}
	}
	sats := []string{"Terra", "Aqua", "SNPP", "NOAA20", "NOAA21"}
	fmt.Printf("%-5s", "year")
	for _, s := range sats {
		fmt.Printf("%9s", s)
	}
	fmt.Println()
	for _, y := range slices.Sorted(maps.Keys(years)) {
		fmt.Printf("%-5s", y)
		for _, s := range sats {
			fmt.Printf("%9d", counts[key{y, s}])
		}
		fmt.Println()
	}
	fmt.Printf("\nfiles read            %9d\n", st.files)
	fmt.Printf("rows read             %9d\n", st.read)
	fmt.Printf("outside the border    %9d\n", st.outside)
	fmt.Printf("NRT replaced by SP    %9d\n", st.nrtOverSP)
	fmt.Printf("exact duplicates      %9d\n", st.duplicates)
	fmt.Printf("without land cover    %9d\n", st.noLandCover)
	fmt.Printf("rows written          %9d (SP %d, NRT %d)\n", len(hs), len(hs)-nrt, nrt)
	if len(hs) > 0 {
		fmt.Printf("dates                 %s … %s\n", hs[0].date, hs[len(hs)-1].date)
	}
}
