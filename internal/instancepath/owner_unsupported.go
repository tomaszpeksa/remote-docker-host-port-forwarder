//go:build !linux && !darwin

package instancepath

import (
	"fmt"
	"os"
)

func openOwnerFile(*os.Root) (*os.File, error) {
	return nil, fmt.Errorf("publisher ownership requires local Linux/macOS storage")
}
