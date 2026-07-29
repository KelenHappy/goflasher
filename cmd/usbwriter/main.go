//go:build !fyne

package main

import (
	"fmt"

	"github.com/goflasher/goflasher/internal/i18n"
)

func main() {
	fmt.Println(i18n.System().T("launcher"))
}
