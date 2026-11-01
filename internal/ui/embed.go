// Package ui embeds the HTML templates and local static assets (no CDN, D3).
package ui

import "embed"

//go:embed templates static
var FS embed.FS
