//go:build deps

// Package deps pins every third-party module the parallel engineers need, so
// nobody edits go.mod concurrently. It is never compiled into hlp (the
// "deps" build tag is never set), but `go mod tidy` keeps these
// requirements because tidy considers all build tags.
//
// To add a dependency, add it here and run `go mod tidy` in the same commit.
package deps

import (
	_ "charm.land/bubbles/v2/textarea"
	_ "charm.land/bubbles/v2/viewport"
	_ "charm.land/bubbletea/v2"
	_ "charm.land/fang/v2"
	_ "charm.land/glamour/v2"
	_ "charm.land/huh/v2"
	_ "charm.land/lipgloss/v2"
	_ "github.com/charmbracelet/colorprofile"
	_ "github.com/coder/websocket"
	_ "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	_ "github.com/google/jsonschema-go/jsonschema"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/pelletier/go-toml/v2"
	_ "github.com/sahilm/fuzzy"
	_ "github.com/shopspring/decimal"
	_ "github.com/spf13/cobra"
	_ "github.com/vmihailenco/msgpack/v5"
	_ "github.com/zalando/go-keyring"
	_ "golang.org/x/crypto/sha3"
	_ "golang.org/x/image/font/basicfont"
	_ "gopkg.in/yaml.v3"
)
