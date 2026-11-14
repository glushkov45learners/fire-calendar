package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"math"
	"os"
)

// border answers point-in-polygon queries against a country outline.
//
// The outline has tens of thousands of vertices, so edges are bucketed into
// thin latitude bands: a query only looks at the edges crossing its own band.
type border struct {
	lat0, bandH float64
	bands       [][]edge
}

type edge struct{ x1, y1, x2, y2 float64 }

type geoJSON struct {
	Features []struct {
		Geometry struct {
			Type        string         `json:"type"`
			Coordinates jsontext.Value `json:"coordinates"`
		} `json:"geometry"`
	} `json:"features"`
}

func loadBorder(path string) (*border, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var g geoJSON
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var polygons [][][][]float64
	for _, f := range g.Features {
		switch f.Geometry.Type {
		case "Polygon":
			var p [][][]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &p); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			polygons = append(polygons, p)
		case "MultiPolygon":
			var mp [][][][]float64
			if err := json.Unmarshal(f.Geometry.Coordinates, &mp); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			polygons = append(polygons, mp...)
		default:
			return nil, fmt.Errorf("%s: unsupported geometry %q", path, f.Geometry.Type)
		}
	}
	var rings [][][]float64
	for _, p := range polygons {
		rings = append(rings, p...)
	}
	if len(rings) == 0 {
		return nil, fmt.Errorf("%s: no polygons", path)
	}
	return newBorder(rings), nil
}

// newBorder builds the index from rings of [lon, lat] points. Outer rings and
// holes are treated alike: the even-odd rule sorts them out.
func newBorder(rings [][][]float64) *border {
	const bandH = 0.02
	lat0, lat1 := math.Inf(1), math.Inf(-1)
	for _, r := range rings {
		for _, p := range r {
			lat0, lat1 = min(lat0, p[1]), max(lat1, p[1])
		}
	}
	b := &border{lat0: lat0, bandH: bandH}
	b.bands = make([][]edge, int((lat1-lat0)/bandH)+1)
	for _, r := range rings {
		for i := range r {
			p, q := r[i], r[(i+1)%len(r)]
			if p[1] == q[1] {
				continue // horizontal edges never cross a horizontal ray
			}
			e := edge{p[0], p[1], q[0], q[1]}
			for k := b.band(min(p[1], q[1])); k <= b.band(max(p[1], q[1])); k++ {
				b.bands[k] = append(b.bands[k], e)
			}
		}
	}
	return b
}

func (b *border) band(lat float64) int {
	return min(max(int((lat-b.lat0)/b.bandH), 0), len(b.bands)-1)
}

// contains reports whether the point lies inside the outline (even-odd rule).
func (b *border) contains(lon, lat float64) bool {
	if lat < b.lat0 || lat > b.lat0+float64(len(b.bands))*b.bandH {
		return false
	}
	inside := false
	for _, e := range b.bands[b.band(lat)] {
		if (e.y1 > lat) != (e.y2 > lat) && lon < e.x1+(lat-e.y1)*(e.x2-e.x1)/(e.y2-e.y1) {
			inside = !inside
		}
	}
	return inside
}
