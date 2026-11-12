package store

import (
	"math"
	"testing"
)

func TestGeoEncodeDecodeMatchesRedis(t *testing.T) {
	cases := []struct {
		lon, lat float64
		score    float64
	}{
		{13.361389, 38.115556, 3479099956230698},
		{15.087269, 37.502669, 3479447370796909},
	}
	for _, tc := range cases {
		score := GeoEncode(tc.lon, tc.lat)
		if score != tc.score {
			t.Fatalf("GeoEncode(%v,%v) = %.0f, want %.0f", tc.lon, tc.lat, score, tc.score)
		}
		lon, lat := GeoDecode(score)
		if math.Abs(lon-tc.lon) > 1e-5 || math.Abs(lat-tc.lat) > 1e-5 {
			t.Fatalf("GeoDecode = %v,%v for %v,%v", lon, lat, tc.lon, tc.lat)
		}
	}
}

func TestGeoDistanceAndHash(t *testing.T) {
	lon1, lat1 := GeoDecode(GeoEncode(13.361389, 38.115556))
	lon2, lat2 := GeoDecode(GeoEncode(15.087269, 37.502669))
	dist := GeoDistance(lon1, lat1, lon2, lat2)
	if math.Abs(dist-166274.15156960033) > 1e-3 {
		t.Fatalf("distance = %v", dist)
	}
	if got := GeoHashString(13.361389, 38.115556); got != "sqc8b49rny0" {
		t.Fatalf("geohash = %q", got)
	}
	if got := GeoHashString(15.087269, 37.502669); got != "sqdtr74hyu0" {
		t.Fatalf("geohash = %q", got)
	}
}

func TestGeoStoreAndSearch(t *testing.T) {
	s, _ := newTestStore()
	pairs := []ScoreMember{
		{GeoEncode(13.361389, 38.115556), "Palermo"},
		{GeoEncode(15.087269, 37.502669), "Catania"},
	}
	if _, err := s.ZAdd("Sicily", ZAddOptions{}, pairs, false); err != nil {
		t.Fatal(err)
	}

	shape := GeoShape{CenterLon: 15, CenterLat: 37, RadiusMeters: 200000}
	points, err := s.GeoSearch("Sicily", shape, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("GeoSearch = %+v", points)
	}

	n, err := s.GeoStore("dst", points, true)
	if err != nil || n != 2 {
		t.Fatalf("GeoStore = %d, %v", n, err)
	}
	if card, _ := s.ZCard("dst"); card != 2 {
		t.Fatalf("destination card = %d", card)
	}

	if _, err := s.Set("str", []byte("v"), SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GeoSearch("str", shape, 0); err != ErrWrongType {
		t.Fatalf("want WRONGTYPE, got %v", err)
	}
}
