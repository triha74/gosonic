package lib

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvalWhen(t *testing.T) {
	t.Setenv("NEO4J_URI", "bolt://db")
	t.Setenv("DISABLED", "false")

	ok, err := EvalWhen("", "")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvalWhen("env:NEO4J_URI", "")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvalWhen("env:NEO4J_URI=bolt://db", "")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvalWhen("!env:MISSING", "")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvalWhen("env:DISABLED", "")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = EvalWhen("always", "failure")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = EvalWhen("success", "failure")
	require.NoError(t, err)
	assert.False(t, ok)

	ok, err = EvalWhen("failure", "failure")
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = EvalWhen("nope", "")
	assert.Error(t, err)
}
