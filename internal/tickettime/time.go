package tickettime

import (
	"errors"
	"strings"
	"time"
)

const displayLayout = "2006-01-02 15:04:05"

var shanghaiLocation = loadShanghaiLocation()

func loadShanghaiLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return location
}

func Location() *time.Location {
	return shanghaiLocation
}

func Parse(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("时间为空")
	}

	rfc3339Formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
	}
	var lastErr error
	for _, format := range rfc3339Formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}

	businessTimeFormats := []string{
		displayLayout,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04",
		"2006-01-02T15:04",
	}
	for _, format := range businessTimeFormats {
		parsed, err := time.ParseInLocation(format, value, Location())
		if err == nil {
			return parsed, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func FormatUnix(timestamp int64) string {
	if timestamp <= 0 {
		return ""
	}
	if timestamp > 1_000_000_000_000 {
		timestamp /= 1000
	}
	return time.Unix(timestamp, 0).In(Location()).Format(displayLayout)
}
