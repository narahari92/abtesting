// Package web embeds the browser snippet, the demo page and the dashboard
// so the service ships as a single binary.
package web

import "embed"

// Files holds every static asset served by the Go binary.
//
//go:embed ab.js demo/*
var Files embed.FS

// BaseURLPlaceholder is the token in ab.js that the server replaces with
// the host the snippet should fetch payloads from.
const BaseURLPlaceholder = "__AB_BASE_URL__"
