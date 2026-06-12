//go:build windows

package app

import (
	"syscall"
	"unsafe"
)

func enableConsoleColors() {
	if !useColor {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := kernel32.NewProc("GetConsoleMode")
	setMode := kernel32.NewProc("SetConsoleMode")

	handle, err := syscall.GetStdHandle(syscall.STD_OUTPUT_HANDLE)
	if err != nil {
		return
	}

	var mode uint32
	r, _, _ := getMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if r == 0 {
		return
	}

	const enableVirtualTerminalProcessing = 0x0004
	setMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
}
