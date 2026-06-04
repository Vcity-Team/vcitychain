//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
)

func pauseForUser() {
	fmt.Fprintln(os.Stderr, "\nPress Enter to exit...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
