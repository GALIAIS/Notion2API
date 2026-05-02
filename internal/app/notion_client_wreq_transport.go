package app

import _ "embed"

var (
	//go:embed assets/browser-helper.cjs
	embeddedNodeWreqHelperScript string

	//go:embed assets/browser-login-helper.cjs
	embeddedNodeWreqLoginHelperScript string
)

func nodeWreqHelperScript() string {
	return embeddedNodeWreqHelperScript
}

func nodeWreqLoginHelperScript() string {
	return embeddedNodeWreqLoginHelperScript
}
