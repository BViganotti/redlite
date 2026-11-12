package server

import (
	"strings"
	"testing"
)

func TestGeoCommandFamily(t *testing.T) {
	c := dial(t, newTestServer(t, nil))

	expectInt(t, c.do("GEOADD", "Sicily", "13.361389", "38.115556", "Palermo",
		"15.087269", "37.502669", "Catania"), 2)

	positions := expectArray(t, c.do("GEOPOS", "Sicily", "Palermo", "Catania", "nope"))
	if len(positions) != 3 {
		t.Fatalf("GEOPOS = %v", positions)
	}
	palermo := expectArray(t, positions[0])
	expectBulk(t, palermo[0], "13.361389338970184")
	expectBulk(t, palermo[1], "38.1155563954963")
	expectNull(t, positions[2])

	expectBulk(t, c.do("GEODIST", "Sicily", "Palermo", "Catania"), "166274.1516")
	expectBulk(t, c.do("GEODIST", "Sicily", "Palermo", "Catania", "km"), "166.2742")
	expectNull(t, c.do("GEODIST", "Sicily", "Palermo", "nope"))

	hashes := expectArray(t, c.do("GEOHASH", "Sicily", "Palermo", "Catania"))
	expectBulk(t, hashes[0], "sqc8b49rny0")
	expectBulk(t, hashes[1], "sqdtr74hyu0")

	results := expectArray(t, c.do("GEOSEARCH", "Sicily", "FROMLONLAT", "15", "37",
		"BYRADIUS", "200", "km", "ASC", "WITHDIST", "WITHHASH", "WITHCOORD"))
	if len(results) != 2 {
		t.Fatalf("GEOSEARCH = %v", results)
	}
	first := expectArray(t, results[0])
	expectBulk(t, first[0], "Catania")
	expectBulk(t, first[1], "56.4413")
	hash := first[2].(int64)
	if hash != 3479447370796909 {
		t.Fatalf("hash = %d", hash)
	}
	coord := expectArray(t, first[3])
	expectBulk(t, coord[0], "15.087267458438873")

	box := expectArray(t, c.do("GEOSEARCH", "Sicily", "FROMMEMBER", "Palermo",
		"BYBOX", "400", "400", "km", "ASC"))
	expectBulk(t, box[0], "Palermo")

	expectInt(t, c.do("GEOSEARCHSTORE", "dst", "Sicily", "FROMLONLAT", "15", "37",
		"BYRADIUS", "200", "km", "ASC", "STOREDIST"), 2)
	stored := expectArray(t, c.do("ZRANGE", "dst", "0", "-1"))
	expectBulk(t, stored[0], "Catania")
	expectBulk(t, stored[1], "Palermo")

	expectInt(t, c.do("GEORADIUS", "Sicily", "15", "37", "200", "km", "ASC", "STORE", "legacy"), 2)
	limited := expectArray(t, c.do("GEORADIUSBYMEMBER", "Sicily", "Palermo", "200", "km", "COUNT", "1"))
	if len(limited) != 1 {
		t.Fatalf("GEORADIUSBYMEMBER COUNT = %v", limited)
	}

	expectError(t, c.do("GEOADD", "Sicily", "181", "0", "bad"), "invalid longitude")
	expectError(t, c.do("GEOADD", "Sicily", "0", "86", "bad"), "invalid longitude")
	expectError(t, c.do("GEODIST", "Sicily", "Palermo", "Catania", "yd"), "unsupported unit")
	expectError(t, c.do("GEOSEARCH", "Sicily", "FROMMEMBER", "nope", "BYRADIUS", "1", "km"), "could not decode")
	expectError(t, c.do("GEOSEARCH", "Sicily", "FROMLONLAT", "0", "0", "BYRADIUS", "1", "km", "ANY"), "ANY argument requires COUNT")
	expectError(t, c.do("GEOSEARCHSTORE", "d", "Sicily", "FROMLONLAT", "0", "0", "BYRADIUS", "1", "km", "WITHCOORD"), "not compatible")

	expectSimple(t, c.do("SET", "str", "v"), "OK")
	expectError(t, c.do("GEOPOS", "str", "x"), "WRONGTYPE")
}

func TestGeoReplyShapeRESP3(t *testing.T) {
	c := dial(t, newTestServer(t, nil))
	c.do("HELLO", "3")
	c.do("GEOADD", "Sicily", "13.361389", "38.115556", "Palermo")

	pos := expectArray(t, c.do("GEOPOS", "Sicily", "Palermo"))[0]
	coords := expectArray(t, pos)
	if !strings.HasPrefix(string(coords[0].([]byte)), "13.36") {
		t.Fatalf("coords = %v", coords)
	}
}
