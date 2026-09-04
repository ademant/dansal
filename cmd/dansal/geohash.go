package main

import "math"

const geohashBase32 = "0123456789bcdefghjkmnpqrstuvwxyz"

// geohashEncode encodes lat/lng to a geohash of the given precision (chars).
func geohashEncode(lat, lng float64, precision int) string {
	minLat, maxLat := -90.0, 90.0
	minLng, maxLng := -180.0, 180.0
	bits := 0
	bit := 0
	ch := 0
	result := make([]byte, precision)
	pos := 0
	isLng := true
	for pos < precision {
		if isLng {
			mid := (minLng + maxLng) / 2
			if lng >= mid {
				ch = ch*2 + 1
				minLng = mid
			} else {
				ch = ch * 2
				maxLng = mid
			}
		} else {
			mid := (minLat + maxLat) / 2
			if lat >= mid {
				ch = ch*2 + 1
				minLat = mid
			} else {
				ch = ch * 2
				maxLat = mid
			}
		}
		isLng = !isLng
		bits++
		if bits == 5 {
			result[pos] = geohashBase32[ch]
			pos++
			bits = 0
			ch = 0
		}
		bit++
		_ = bit
	}
	return string(result)
}

// geohashBBox returns the bounding box for a geohash prefix.
func geohashBBox(hash string) (minLat, maxLat, minLng, maxLng float64) {
	minLat, maxLat = -90.0, 90.0
	minLng, maxLng = -180.0, 180.0
	isLng := true
	for _, c := range hash {
		idx := indexInBase32(byte(c))
		if idx < 0 {
			break
		}
		for i := 4; i >= 0; i-- {
			bit := (idx >> uint(i)) & 1
			if isLng {
				mid := (minLng + maxLng) / 2
				if bit == 1 {
					minLng = mid
				} else {
					maxLng = mid
				}
			} else {
				mid := (minLat + maxLat) / 2
				if bit == 1 {
					minLat = mid
				} else {
					maxLat = mid
				}
			}
			isLng = !isLng
		}
	}
	return
}

func indexInBase32(c byte) int {
	for i := range len(geohashBase32) {
		if geohashBase32[i] == c {
			return i
		}
	}
	return -1
}

// geohashDecode returns the centre lat/lng of a geohash.
func geohashDecode(hash string) (lat, lng float64) {
	minLat, maxLat, minLng, maxLng := geohashBBox(hash)
	return (minLat + maxLat) / 2, (minLng + maxLng) / 2
}

// geohashRadiusToBBox converts a centre point + radius (km) to a bounding box.
func geohashRadiusToBBox(lat, lng, radiusKm float64) (minLat, maxLat, minLng, maxLng float64) {
	dLat := radiusKm / 111.0
	dLng := radiusKm / (111.0 * math.Cos(lat*math.Pi/180))
	return lat - dLat, lat + dLat, lng - dLng, lng + dLng
}
