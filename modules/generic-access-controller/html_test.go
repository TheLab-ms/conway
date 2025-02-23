package gac

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSwipesList(t *testing.T) {
	responseFixture, err := os.Open(filepath.Join("fixtures", "swipes", "response.html"))
	require.NoError(t, err)
	defer responseFixture.Close()

	actual, err := parseSwipesList(responseFixture)
	require.NoError(t, err)

	expectedFixture, err := os.Open(filepath.Join("fixtures", "swipes", "expected.json"))
	require.NoError(t, err)
	defer expectedFixture.Close()

	expected := []*CardSwipe{}
	err = json.NewDecoder(expectedFixture).Decode(&expected)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}

func TestParseSwipesNoTable(t *testing.T) {
	_, err := parseSwipesList(bytes.NewBufferString("<body>foo</body>"))
	require.EqualError(t, err, "no table found in access controller response")
}

func TestParseCardsList(t *testing.T) {
	responseFixture, err := os.Open(filepath.Join("fixtures", "cards", "response.html"))
	require.NoError(t, err)
	defer responseFixture.Close()

	actual, err := parseCardsList(responseFixture)
	require.NoError(t, err)

	expectedFixture, err := os.Open(filepath.Join("fixtures", "cards", "expected.json"))
	require.NoError(t, err)
	defer expectedFixture.Close()

	expected := []*Card{}
	err = json.NewDecoder(expectedFixture).Decode(&expected)
	require.NoError(t, err)
	assert.Equal(t, expected, actual)
}
