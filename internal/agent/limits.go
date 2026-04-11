package agent

import (
	"os"
	"strconv"
	"strings"
)

func maxSteps(requested int) int {
	const hardCap = 15
	defaultSteps := 12
	if v := strings.TrimSpace(os.Getenv("AGENT_MAX_STEPS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			defaultSteps = n
		}
	}
	max := defaultSteps
	if requested > 0 {
		max = requested
	}
	if max > hardCap {
		max = hardCap
	}
	if max < 1 {
		max = 1
	}
	return max
}
