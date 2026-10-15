package lib

import (
	"fmt"
	"strconv"
	"strings"
)

// CoveragePercent reads a coverage file. format "percent" is a single number.
// format "go" is a Go cover profile. An empty format means percent.
func CoveragePercent(data, format string) (float64, error) {
	switch format {
	case "", "percent":
		value, err := strconv.ParseFloat(strings.TrimSpace(data), 64)
		if err != nil {
			return 0, fmt.Errorf("parsing coverage percent: %w", err)
		}
		return value, nil
	case "go":
		return goCoverPercent(data)
	default:
		return 0, fmt.Errorf("unknown coverage format %q", format)
	}
}

func goCoverPercent(data string) (float64, error) {
	var total, covered int
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return 0, fmt.Errorf("invalid cover profile line %q", line)
		}
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return 0, fmt.Errorf("invalid statement count in %q", line)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return 0, fmt.Errorf("invalid execution count in %q", line)
		}
		total += stmts
		if count > 0 {
			covered += stmts
		}
	}
	if total == 0 {
		return 0, fmt.Errorf("cover profile has no statements")
	}
	return float64(covered) / float64(total) * 100, nil
}
