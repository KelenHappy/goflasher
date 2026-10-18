//go:build !wimlib

package native

import "fmt"

const linkage = "binary built without wimlib"

func linkedFunctions() (functions, error) {
	return functions{}, fmt.Errorf("%w: %s", ErrUnsupported, linkage)
}
