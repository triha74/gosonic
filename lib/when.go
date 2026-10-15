package lib

import (
	"fmt"
	"os"
	"strings"
)

// EvalWhen reports whether a stage or hook should run.
// An empty expression is true. after hooks pass outcome "success" or "failure".
// Supported forms are always, success, failure, env:NAME, env:NAME=value, and a leading !.
func EvalWhen(expr, outcome string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}
	neg := false
	if strings.HasPrefix(expr, "!") {
		neg = true
		expr = strings.TrimSpace(strings.TrimPrefix(expr, "!"))
	}

	var ok bool
	switch expr {
	case "always":
		ok = true
	case "success":
		ok = outcome == "" || outcome == "success"
	case "failure":
		ok = outcome == "failure"
	default:
		if !strings.HasPrefix(expr, "env:") {
			return false, fmt.Errorf("invalid when expression %q", expr)
		}
		rest := strings.TrimPrefix(expr, "env:")
		name, want, hasValue := strings.Cut(rest, "=")
		if name == "" {
			return false, fmt.Errorf("invalid when expression %q", expr)
		}
		got, exists := os.LookupEnv(name)
		if !hasValue {
			ok = exists && got != "" && got != "0" && !strings.EqualFold(got, "false")
		} else {
			ok = exists && got == want
		}
	}
	if neg {
		return !ok, nil
	}
	return ok, nil
}
