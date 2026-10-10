//go:build !linux && !darwin

package instancepath

import (
	"fmt"
	"os"
)

func openOwnerDirectory(string) (*os.File, error) {
	return nil, fmt.Errorf("publisher ownership requires local Linux/macOS storage")
}

func openOwnerFile(*os.File) (*os.File, error) {
	return nil, fmt.Errorf("publisher ownership requires local Linux/macOS storage")
}
