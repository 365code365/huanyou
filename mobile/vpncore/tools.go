//go:build tools

package vpncore

// Keeps golang.org/x/mobile in go.mod; gomobile bind requires it.
import _ "golang.org/x/mobile/bind"
