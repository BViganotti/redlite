package store

import "math"

// Redis' geohash parameters. GEO keys are ordinary sorted sets whose scores
// are 52-bit interleaved latitude/longitude values.
const (
	geoLatMin  = -85.05112878
	geoLatMax  = 85.05112878
	geoLonMin  = -180.0
	geoLonMax  = 180.0
	geoStep    = 26
	geoMaxBits = uint64(1) << (geoStep * 2)
	// earthRadiusMeters is the value Redis uses in geohash_helper.c.
	earthRadiusMeters = 6372797.560856
)

// GeoEncode converts a longitude/latitude pair into the score Redis stores.
func GeoEncode(lon, lat float64) float64 {
	latOffset := (lat - geoLatMin) / (geoLatMax - geoLatMin)
	lonOffset := (lon - geoLonMin) / (geoLonMax - geoLonMin)
	latOffset *= float64(uint64(1) << geoStep)
	lonOffset *= float64(uint64(1) << geoStep)
	return float64(interleave64(uint32(latOffset), uint32(lonOffset)))
}

// GeoDecode converts a stored score back into the centre of its geohash cell.
func GeoDecode(score float64) (lon, lat float64) {
	if score < 0 {
		return 0, 0
	}
	sep := deinterleave64(uint64(score))
	ilat := uint32(sep)
	ilon := uint32(sep >> 32)
	scale := float64(uint64(1) << geoStep)

	latMin := geoLatMin + float64(ilat)/scale*(geoLatMax-geoLatMin)
	latMax := geoLatMin + float64(ilat+1)/scale*(geoLatMax-geoLatMin)
	lonMin := geoLonMin + float64(ilon)/scale*(geoLonMax-geoLonMin)
	lonMax := geoLonMin + float64(ilon+1)/scale*(geoLonMax-geoLonMin)

	lon = (lonMin + lonMax) / 2
	lat = (latMin + latMax) / 2
	if lon > geoLonMax {
		lon = geoLonMax
	}
	if lon < geoLonMin {
		lon = geoLonMin
	}
	if lat > geoLatMax {
		lat = geoLatMax
	}
	if lat < geoLatMin {
		lat = geoLatMin
	}
	return lon, lat
}

func interleave64(xlo, ylo uint32) uint64 {
	b := [5]uint64{0x5555555555555555, 0x3333333333333333, 0x0F0F0F0F0F0F0F0F, 0x00FF00FF00FF00FF, 0x0000FFFF0000FFFF}
	s := [5]uint{1, 2, 4, 8, 16}
	x, y := uint64(xlo), uint64(ylo)
	x = (x | (x << s[4])) & b[4]
	y = (y | (y << s[4])) & b[4]
	x = (x | (x << s[3])) & b[3]
	y = (y | (y << s[3])) & b[3]
	x = (x | (x << s[2])) & b[2]
	y = (y | (y << s[2])) & b[2]
	x = (x | (x << s[1])) & b[1]
	y = (y | (y << s[1])) & b[1]
	x = (x | (x << s[0])) & b[0]
	y = (y | (y << s[0])) & b[0]
	return x | (y << 1)
}

func deinterleave64(interleaved uint64) uint64 {
	b := [6]uint64{0x5555555555555555, 0x3333333333333333, 0x0F0F0F0F0F0F0F0F, 0x00FF00FF00FF00FF, 0x0000FFFF0000FFFF, 0x00000000FFFFFFFF}
	s := [6]uint{0, 1, 2, 4, 8, 16}
	x, y := interleaved, interleaved>>1
	x = (x | (x >> s[0])) & b[0]
	y = (y | (y >> s[0])) & b[0]
	x = (x | (x >> s[1])) & b[1]
	y = (y | (y >> s[1])) & b[1]
	x = (x | (x >> s[2])) & b[2]
	y = (y | (y >> s[2])) & b[2]
	x = (x | (x >> s[3])) & b[3]
	y = (y | (y >> s[3])) & b[3]
	x = (x | (x >> s[4])) & b[4]
	y = (y | (y >> s[4])) & b[4]
	x = (x | (x >> s[5])) & b[5]
	y = (y | (y >> s[5])) & b[5]
	return x | (y << 32)
}

func degRad(deg float64) float64 { return deg * math.Pi / 180.0 }

// GeoDistance returns the great-circle distance in metres between two points.
func GeoDistance(lon1, lat1, lon2, lat2 float64) float64 {
	lon1r := degRad(lon1)
	lon2r := degRad(lon2)
	v := math.Sin((lon2r - lon1r) / 2)
	if v == 0 {
		return earthRadiusMeters * math.Abs(degRad(lat2)-degRad(lat1))
	}
	lat1r := degRad(lat1)
	lat2r := degRad(lat2)
	u := math.Sin((lat2r - lat1r) / 2)
	a := u*u + math.Cos(lat1r)*math.Cos(lat2r)*v*v
	return 2.0 * earthRadiusMeters * math.Asin(math.Sqrt(a))
}

// GeoLatDistance returns the north/south distance in metres.
func GeoLatDistance(lat1, lat2 float64) float64 {
	return earthRadiusMeters * math.Abs(degRad(lat2)-degRad(lat1))
}

// GeoHashString renders the standard 11-character geohash Redis returns from
// GEOHASH. Geohash uses the -90..90 latitude range, unlike the internal score.
func GeoHashString(lon, lat float64) string {
	const alphabet = "0123456789bcdefghjkmnpqrstuvwxyz"
	latOffset := (lat - (-90.0)) / 180.0
	lonOffset := (lon - (-180.0)) / 360.0
	latOffset *= float64(uint64(1) << 26)
	lonOffset *= float64(uint64(1) << 26)
	bits := interleave64(uint32(latOffset), uint32(lonOffset))

	buf := make([]byte, 11)
	for i := 0; i < 11; i++ {
		if i == 10 {
			buf[i] = alphabet[0]
			continue
		}
		idx := (bits >> (52 - uint((i+1)*5))) & 0x1f
		buf[i] = alphabet[idx]
	}
	return string(buf)
}

// GeoShape describes a radius or box search.
type GeoShape struct {
	CenterLon    float64
	CenterLat    float64
	RadiusMeters float64
	Box          bool
	WidthMeters  float64
	HeightMeters float64
}

// GeoPoint is one search result. Score is the original geohash score.
type GeoPoint struct {
	Member string
	Score  float64
	Lon    float64
	Lat    float64
	Dist   float64 // metres
}

// GeoSearch returns the members of a sorted set inside shape. When anyLimit is
// positive, collection stops as soon as that many matches are found, which is
// the ANY optimisation; otherwise the caller sorts and limits.
func (s *Store) GeoSearch(key string, shape GeoShape, anyLimit int) ([]GeoPoint, error) {
	now := s.now()
	sh := s.lockRead(key)
	defer s.unlockRead(sh)

	e, ok := s.lookupRead(sh, key, now)
	if !ok {
		return nil, nil
	}
	zv, ok := e.value.(*zsetValue)
	if !ok {
		return nil, ErrWrongType
	}

	out := make([]GeoPoint, 0, 16)
	for node := zv.list.head.levels[0].forward; node != nil; node = node.levels[0].forward {
		lon, lat := GeoDecode(node.score)
		dist := GeoDistance(shape.CenterLon, shape.CenterLat, lon, lat)
		if shape.Box {
			if GeoLatDistance(lat, shape.CenterLat) > shape.HeightMeters/2 {
				continue
			}
			if GeoDistance(lon, lat, shape.CenterLon, lat) > shape.WidthMeters/2 {
				continue
			}
		} else if dist > shape.RadiusMeters {
			continue
		}
		out = append(out, GeoPoint{
			Member: node.member,
			Score:  node.score,
			Lon:    lon,
			Lat:    lat,
			Dist:   dist,
		})
		if anyLimit > 0 && len(out) >= anyLimit {
			break
		}
	}
	return out, nil
}

// GeoMember returns the decoded position of one member.
func (s *Store) GeoMember(key, member string) (GeoPoint, bool, error) {
	score, ok, err := s.ZScore(key, member)
	if err != nil || !ok {
		return GeoPoint{}, false, err
	}
	lon, lat := GeoDecode(score)
	return GeoPoint{Member: member, Score: score, Lon: lon, Lat: lat}, true, nil
}

// GeoStore replaces destination with the supplied points. The score is the
// distance when storeDist is set, otherwise the original geohash score.
func (s *Store) GeoStore(destination string, points []GeoPoint, storeDist bool) (int64, error) {
	locked := s.lockAllShards()
	defer s.unlockAllShards(locked)

	destShard := s.shardFor(destination)
	if len(points) == 0 {
		delete(destShard.data, destination)
		return 0, nil
	}
	zv := newZSetValue()
	for _, point := range points {
		score := point.Score
		if storeDist {
			score = point.Dist
		}
		zv.dict[point.Member] = score
		zv.list.insert(point.Member, score)
	}
	destShard.data[destination] = s.newEntry(zv, 0)
	s.sets.Add(1)
	return int64(len(points)), nil
}
