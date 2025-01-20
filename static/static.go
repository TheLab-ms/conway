// Package static contains static assets like css, js, images, etc.
package static

import "embed"

//go:embed assets/*
var Assets embed.FS
