package lib

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCoveragePercent(t *testing.T) {
	percent, err := CoveragePercent("82.5\n", "percent")
	require.NoError(t, err)
	assert.Equal(t, 82.5, percent)

	percent, err = CoveragePercent("mode: set\nfile.go:1.1,2.2 2 1\nfile.go:3.1,4.2 2 0\n", "go")
	require.NoError(t, err)
	assert.Equal(t, 50.0, percent)

	_, err = CoveragePercent("nope", "percent")
	assert.Error(t, err)
}
