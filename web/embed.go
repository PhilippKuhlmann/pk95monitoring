// Package web holds the web UI (vanilla JS single page app), embedded into the binary.
package web

import "embed"

// FS contains index.html, app.js, style.css, theme.js, the status page (status.html,
// status.js), the logo and the fonts.
//
//go:embed index.html app.js style.css theme.js status.html status.js logo.svg fonts
var FS embed.FS
