package oauth2

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootDomain(t *testing.T) {
	assert.Equal(t, "bar.baz", rootDomain(&url.URL{Host: "foo.bar.baz:8080"}))
	assert.Equal(t, "bar.baz", rootDomain(&url.URL{Host: "foo.bar.baz"}))
	assert.Equal(t, "baz", rootDomain(&url.URL{Host: "baz"}))
}
