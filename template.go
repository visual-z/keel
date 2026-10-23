// Package keel embeds the project template that `keel init` writes. It lives
// in the module root because go:embed cannot reach outside a package's tree.
package keel

import "embed"

// Template holds template/, dotfiles included (.githooks, .gitignore).
//
//go:embed all:template
var Template embed.FS
