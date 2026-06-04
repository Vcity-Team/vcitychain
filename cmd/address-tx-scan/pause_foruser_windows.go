//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pauseForUser() {
	if !hasConsoleWindow() {
		messageBoxDone()
		return
	}
	var mode uint32
	h := windows.Handle(os.Stdin.Fd())
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		messageBoxDone()
		return
	}
	fmt.Fprintln(os.Stderr, "\nPress Enter to exit...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

func hasConsoleWindow() bool {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	pGetConsoleWindow := k32.NewProc("GetConsoleWindow")
	ret, _, _ := pGetConsoleWindow.Call()
	return ret != 0
}

func messageBoxDone() {
	user32 := syscall.NewLazyDLL("user32.dll")
	pMessageBoxW := user32.NewProc("MessageBoxW")
	const mbOK = 0
	title, _ := syscall.UTF16PtrFromString("address-tx-scan")
	text, _ := syscall.UTF16PtrFromString("运行结束。若未看到输出，请检查 -o 指定的文件或 stderr。点「确定」关闭。")
	_, _, _ = pMessageBoxW.Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), mbOK)
}
