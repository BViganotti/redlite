package server

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/BViganotti/redlite/internal/store"
)

func init() {
	registerCommand("GEOADD", -5, flagWrite, cmdGeoAdd)
	registerCommand("GEOPOS", -2, flagReadOnly, cmdGeoPos)
	registerCommand("GEODIST", -4, flagReadOnly, cmdGeoDist)
	registerCommand("GEOHASH", -2, flagReadOnly, cmdGeoHash)
	registerCommand("GEOSEARCH", -7, flagReadOnly, func(c *client, args [][]byte) (result, error) {
		return geoSearchCommand(c, args, false)
	})
	registerCommand("GEOSEARCHSTORE", -8, flagWrite, func(c *client, args [][]byte) (result, error) {
		return geoSearchCommand(c, args, true)
	})
	registerCommand("GEORADIUS", -6, flagWrite, func(c *client, args [][]byte) (result, error) {
		return geoRadiusCommand(c, args, false, false)
	})
	registerCommand("GEORADIUS_RO", -6, flagReadOnly, func(c *client, args [][]byte) (result, error) {
		return geoRadiusCommand(c, args, true, false)
	})
	registerCommand("GEORADIUSBYMEMBER", -5, flagWrite, func(c *client, args [][]byte) (result, error) {
		return geoRadiusCommand(c, args, false, true)
	})
	registerCommand("GEORADIUSBYMEMBER_RO", -5, flagReadOnly, func(c *client, args [][]byte) (result, error) {
		return geoRadiusCommand(c, args, true, true)
	})
}

var errGeoUnit = fmt.Errorf("ERR unsupported unit provided. please use M, KM, FT, MI")

func geoUnit(raw []byte) (float64, error) {
	switch strings.ToLower(string(raw)) {
	case "m":
		return 1, nil
	case "km":
		return 1000, nil
	case "ft":
		return 0.3048, nil
	case "mi":
		return 1609.34, nil
	}
	return 0, errGeoUnit
}

func geoFloat(raw []byte, message string) (float64, error) {
	value, err := strconv.ParseFloat(string(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("ERR %s", message)
	}
	return value, nil
}

func cmdGeoAdd(c *client, args [][]byte) (result, error) {
	opts := store.ZAddOptions{}
	index := 2
	for index < len(args) {
		switch strings.ToUpper(string(args[index])) {
		case "NX":
			opts.NX = true
		case "XX":
			opts.XX = true
		case "CH":
			opts.CH = true
		default:
			goto parsed
		}
		index++
	}
parsed:
	if opts.NX && opts.XX {
		return result{}, errSyntax
	}
	if len(args)-index == 0 || (len(args)-index)%3 != 0 {
		return result{}, errSyntax
	}

	pairs := make([]store.ScoreMember, 0, (len(args)-index)/3)
	for i := index; i < len(args); i += 3 {
		lon, err := geoFloat(args[i], "value is not a valid float")
		if err != nil {
			return result{}, err
		}
		lat, err := geoFloat(args[i+1], "value is not a valid float")
		if err != nil {
			return result{}, err
		}
		if lon < -180 || lon > 180 || lat < -85.05112878 || lat > 85.05112878 {
			return result{}, fmt.Errorf("ERR invalid longitude,latitude pair %f,%f", lon, lat)
		}
		pairs = append(pairs, store.ScoreMember{Score: store.GeoEncode(lon, lat), Member: string(args[i+2])})
	}
	res, err := c.srv.data.ZAdd(string(args[1]), opts, pairs, false)
	if err != nil {
		return result{}, err
	}
	count := res.Added
	if opts.CH {
		count = res.Changed
	}
	return result{reply: intReply(count), mutated: res.Changed > 0}, nil
}

func cmdGeoPos(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-2)
	for _, raw := range args[2:] {
		point, ok, err := c.srv.data.GeoMember(string(args[1]), string(raw))
		if err != nil {
			return result{}, err
		}
		if !ok {
			out = append(out, nullArrayReply{})
			continue
		}
		out = append(out, arrayReply{doubleReply(point.Lon), doubleReply(point.Lat)})
	}
	return result{reply: out}, nil
}

func cmdGeoDist(c *client, args [][]byte) (result, error) {
	if len(args) > 5 {
		return result{}, errSyntax
	}
	factor := 1.0
	if len(args) == 5 {
		var err error
		factor, err = geoUnit(args[4])
		if err != nil {
			return result{}, err
		}
	}
	first, ok, err := c.srv.data.GeoMember(string(args[1]), string(args[2]))
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	second, ok, err := c.srv.data.GeoMember(string(args[1]), string(args[3]))
	if err != nil {
		return result{}, err
	}
	if !ok {
		return result{reply: null}, nil
	}
	distance := store.GeoDistance(first.Lon, first.Lat, second.Lon, second.Lat) / factor
	return result{reply: bulkReply(formatGeoDistance(distance))}, nil
}

func formatGeoDistance(meters float64) string {
	return strconv.FormatFloat(meters, 'f', 4, 64)
}

func cmdGeoHash(c *client, args [][]byte) (result, error) {
	out := make(arrayReply, 0, len(args)-2)
	for _, raw := range args[2:] {
		point, ok, err := c.srv.data.GeoMember(string(args[1]), string(raw))
		if err != nil {
			return result{}, err
		}
		if !ok {
			out = append(out, null)
			continue
		}
		out = append(out, bulkReply(store.GeoHashString(point.Lon, point.Lat)))
	}
	return result{reply: out}, nil
}

type geoOptions struct {
	sortAsc   bool
	sortDesc  bool
	count     int
	any       bool
	withCoord bool
	withDist  bool
	withHash  bool
	storeKey  string
	storeDist bool
	hasStore  bool
}

func (o *geoOptions) parseCommon(args [][]byte, i int, allowWith, allowStore bool) (int, error) {
	switch strings.ToUpper(string(args[i])) {
	case "ASC":
		o.sortAsc = true
	case "DESC":
		o.sortDesc = true
	case "ANY":
		o.any = true
	case "COUNT":
		if i+1 >= len(args) {
			return i, errSyntax
		}
		n, err := parseInt(args[i+1])
		if err != nil {
			return i, err
		}
		if n <= 0 {
			return i, fmt.Errorf("ERR COUNT must be > 0")
		}
		o.count = int(n)
		i++
		if i+1 < len(args) && strings.EqualFold(string(args[i+1]), "ANY") {
			o.any = true
			i++
		}
	case "WITHCOORD", "WITHDIST", "WITHHASH":
		if !allowWith {
			return i, fmt.Errorf("ERR GEOSEARCHSTORE is not compatible with WITHDIST, WITHHASH and WITHCOORD options")
		}
		switch strings.ToUpper(string(args[i])) {
		case "WITHCOORD":
			o.withCoord = true
		case "WITHDIST":
			o.withDist = true
		case "WITHHASH":
			o.withHash = true
		}
	case "STORE", "STOREDIST":
		if !allowStore {
			return i, errSyntax
		}
		if o.hasStore {
			return i, errSyntax
		}
		if i+1 >= len(args) {
			return i, errSyntax
		}
		o.storeKey = string(args[i+1])
		o.storeDist = strings.EqualFold(string(args[i]), "STOREDIST")
		o.hasStore = true
		i++
	default:
		return i, errSyntax
	}
	return i, nil
}

func geoSearchCommand(c *client, args [][]byte, storeCmd bool) (result, error) {
	keyIndex := 1
	start := 2
	if storeCmd {
		keyIndex = 2
		start = 3
	}
	source := string(args[keyIndex])
	var (
		shape       store.GeoShape
		shapeFactor = 1.0
		fromMember  string
		hasMember   bool
		hasLonLat   bool
		hasRadius   bool
		hasBox      bool
	)
	opts := geoOptions{}

	for i := start; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "FROMMEMBER":
			if i+1 >= len(args) || hasMember || hasLonLat {
				return result{}, errSyntax
			}
			fromMember = string(args[i+1])
			hasMember = true
			i++
		case "FROMLONLAT":
			if i+2 >= len(args) || hasMember || hasLonLat {
				return result{}, errSyntax
			}
			lon, err := geoFloat(args[i+1], "value is not a valid float")
			if err != nil {
				return result{}, err
			}
			lat, err := geoFloat(args[i+2], "value is not a valid float")
			if err != nil {
				return result{}, err
			}
			shape.CenterLon, shape.CenterLat = lon, lat
			hasLonLat = true
			i += 2
		case "BYRADIUS":
			if i+2 >= len(args) || hasRadius || hasBox {
				return result{}, errSyntax
			}
			radius, err := geoFloat(args[i+1], "need numeric radius")
			if err != nil {
				return result{}, err
			}
			if radius < 0 {
				return result{}, fmt.Errorf("ERR radius cannot be negative")
			}
			factor, err := geoUnit(args[i+2])
			if err != nil {
				return result{}, err
			}
			shape.RadiusMeters = radius * factor
			shapeFactor = factor
			shape.Box = false
			hasRadius = true
			i += 2
		case "BYBOX":
			if i+3 >= len(args) || hasRadius || hasBox {
				return result{}, errSyntax
			}
			width, err := geoFloat(args[i+1], "need numeric width")
			if err != nil {
				return result{}, err
			}
			height, err := geoFloat(args[i+2], "need numeric height")
			if err != nil {
				return result{}, err
			}
			if width < 0 || height < 0 {
				return result{}, fmt.Errorf("ERR height or width cannot be negative")
			}
			factor, err := geoUnit(args[i+3])
			if err != nil {
				return result{}, err
			}
			shape.Box = true
			shape.WidthMeters = width * factor
			shape.HeightMeters = height * factor
			shapeFactor = factor
			hasBox = true
			i += 3
		case "STOREDIST":
			if !storeCmd {
				return result{}, errSyntax
			}
			opts.storeDist = true
		default:
			next, err := opts.parseCommon(args, i, !storeCmd, false)
			if err != nil {
				return result{}, err
			}
			i = next
		}
	}

	command := "GEOSEARCH"
	if storeCmd {
		command = "GEOSEARCHSTORE"
	}
	if hasMember == hasLonLat {
		return result{}, fmt.Errorf("ERR exactly one of FROMMEMBER or FROMLONLAT can be specified for %s", command)
	}
	if hasRadius == hasBox {
		return result{}, fmt.Errorf("ERR exactly one of BYRADIUS and BYBOX can be specified for %s", command)
	}
	if opts.any && opts.count == 0 {
		return result{}, fmt.Errorf("ERR the ANY argument requires COUNT argument")
	}
	if storeCmd && opts.withCoord {
		return result{}, fmt.Errorf("ERR GEOSEARCHSTORE is not compatible with WITHDIST, WITHHASH and WITHCOORD options")
	}

	if hasMember {
		point, ok, err := c.srv.data.GeoMember(source, fromMember)
		if err != nil {
			return result{}, err
		}
		if !ok {
			return result{}, fmt.Errorf("ERR could not decode requested zset member")
		}
		shape.CenterLon, shape.CenterLat = point.Lon, point.Lat
	}

	points, err := c.srv.data.GeoSearch(source, shape, anyLimit(opts))
	if err != nil {
		return result{}, err
	}
	points = sortGeoPoints(points, opts)
	if opts.count > 0 && len(points) > opts.count {
		points = points[:opts.count]
	}

	if storeCmd {
		return storeGeoResults(c, string(args[1]), points, opts, shapeFactor)
	}
	return geoResultsReply(c, points, opts, shapeFactor)
}

// geoRadiusCommand implements GEORADIUS, GEORADIUSBYMEMBER and their _RO
// variants.
func geoRadiusCommand(c *client, args [][]byte, readOnly, byMember bool) (result, error) {
	key := string(args[1])
	var (
		shape  store.GeoShape
		factor float64
		start  int
	)
	if byMember {
		if len(args) < 5 {
			return result{}, errSyntax
		}
		point, ok, err := c.srv.data.GeoMember(key, string(args[2]))
		if err != nil {
			return result{}, err
		}
		if !ok {
			return result{}, fmt.Errorf("ERR could not decode requested zset member")
		}
		radius, err := geoFloat(args[3], "need numeric radius")
		if err != nil {
			return result{}, err
		}
		if radius < 0 {
			return result{}, fmt.Errorf("ERR radius cannot be negative")
		}
		factor, err = geoUnit(args[4])
		if err != nil {
			return result{}, err
		}
		shape.CenterLon, shape.CenterLat = point.Lon, point.Lat
		shape.RadiusMeters = radius * factor
		start = 5
	} else {
		lon, err := geoFloat(args[2], "value is not a valid float")
		if err != nil {
			return result{}, err
		}
		lat, err := geoFloat(args[3], "value is not a valid float")
		if err != nil {
			return result{}, err
		}
		radius, err := geoFloat(args[4], "need numeric radius")
		if err != nil {
			return result{}, err
		}
		if radius < 0 {
			return result{}, fmt.Errorf("ERR radius cannot be negative")
		}
		factor, err = geoUnit(args[5])
		if err != nil {
			return result{}, err
		}
		shape.CenterLon, shape.CenterLat = lon, lat
		shape.RadiusMeters = radius * factor
		start = 6
	}

	opts := geoOptions{}
	for i := start; i < len(args); i++ {
		next, err := opts.parseCommon(args, i, true, !readOnly)
		if err != nil {
			return result{}, err
		}
		i = next
	}
	if opts.any && opts.count == 0 {
		return result{}, fmt.Errorf("ERR the ANY argument requires COUNT argument")
	}
	if opts.hasStore && (opts.withCoord || opts.withDist || opts.withHash) {
		return result{}, fmt.Errorf("ERR STORE option in GEORADIUS is not compatible with WITHDIST, WITHHASH and WITHCOORD options")
	}

	points, err := c.srv.data.GeoSearch(key, shape, anyLimit(opts))
	if err != nil {
		return result{}, err
	}
	points = sortGeoPoints(points, opts)
	if opts.count > 0 && len(points) > opts.count {
		points = points[:opts.count]
	}

	if opts.hasStore {
		return storeGeoResults(c, opts.storeKey, points, opts, factor)
	}
	return geoResultsReply(c, points, opts, factor)
}

func anyLimit(opts geoOptions) int {
	if opts.any && opts.count > 0 {
		return opts.count
	}
	return 0
}

func sortGeoPoints(points []store.GeoPoint, opts geoOptions) []store.GeoPoint {
	asc := opts.sortAsc || (!opts.sortDesc && opts.count > 0 && !opts.any)
	desc := opts.sortDesc
	if !asc && !desc {
		return points
	}
	sorted := append([]store.GeoPoint(nil), points...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if desc {
			return sorted[i].Dist > sorted[j].Dist
		}
		return sorted[i].Dist < sorted[j].Dist
	})
	return sorted
}

func storeGeoResults(c *client, destination string, points []store.GeoPoint, opts geoOptions, factor float64) (result, error) {
	if opts.storeDist {
		for i := range points {
			points[i].Dist /= factor
		}
	}
	n, err := c.srv.data.GeoStore(destination, points, opts.storeDist)
	if err != nil {
		return result{}, err
	}
	return result{reply: intReply(n), mutated: true}, nil
}

func geoResultsReply(c *client, points []store.GeoPoint, opts geoOptions, factor float64) (result, error) {
	nested := opts.withCoord || opts.withDist || opts.withHash
	out := make(arrayReply, 0, len(points))
	for _, point := range points {
		item := make(arrayReply, 0, 4)
		item = append(item, bulkReply(point.Member))
		if opts.withDist {
			item = append(item, bulkReply(formatGeoDistance(point.Dist/factor)))
		}
		if opts.withHash {
			item = append(item, intReply(int64(point.Score)))
		}
		if opts.withCoord {
			item = append(item, arrayReply{doubleReply(point.Lon), doubleReply(point.Lat)})
		}
		if nested {
			out = append(out, item)
		} else {
			out = append(out, bulkReply(point.Member))
		}
	}
	return result{reply: out}, nil
}
