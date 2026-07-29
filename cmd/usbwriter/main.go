//go:build !fyne

package main

import "fmt"

func main() {
	fmt.Println("GoFlasher GUI source is available with the 'fyne' build tag. Install Fyne build dependencies, then run: go run -tags fyne ./cmd/usbwriter")
}
