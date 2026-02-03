//go:build windows

package main

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

var (
	user32                      = syscall.NewLazyDLL("user32.dll")
	sendMessageW                = user32.NewProc("SendMessageW")
	findWindowW                 = user32.NewProc("FindWindowW")
	showWindow                  = user32.NewProc("ShowWindow")
	setForegroundWnd            = user32.NewProc("SetForegroundWindow")
	isWindowVisible             = user32.NewProc("IsWindowVisible")
	isIconic                    = user32.NewProc("IsIconic")
	createIconFromResourceEx    = user32.NewProc("CreateIconFromResourceEx")
	lookupIconIdFromDirectoryEx = user32.NewProc("LookupIconIdFromDirectoryEx")
)

const (
	WM_SETICON      = 0x0080
	ICON_SMALL      = 0
	ICON_BIG        = 1
	SW_HIDE         = 0
	SW_SHOW         = 5
	SW_RESTORE      = 9
	SW_MINIMIZE     = 6
	LR_DEFAULTCOLOR = 0x00000000
)

var mainWindowHandle uintptr

// SetWindowIconFromMemory sets the window icon from embedded icon data
func SetWindowIconFromMemory(icoData []byte) {
	if len(icoData) < 6 {
		return
	}

	// Find the window by title
	titleW, _ := syscall.UTF16PtrFromString("e_n_u_f 2.0")
	hwnd, _, _ := findWindowW.Call(0, uintptr(unsafe.Pointer(titleW)))
	if hwnd == 0 {
		return
	}
	mainWindowHandle = hwnd

	// Parse ICO file format to find the best icons
	// ICO header: 2 bytes reserved, 2 bytes type, 2 bytes count
	numImages := int(binary.LittleEndian.Uint16(icoData[4:6]))
	if numImages == 0 {
		return
	}

	// Find best icon for each size
	var smallIcon, bigIcon uintptr
	var smallBest, bigBest int = -1, -1

	for i := 0; i < numImages; i++ {
		// Each directory entry is 16 bytes starting at offset 6
		entryOffset := 6 + i*16
		if entryOffset+16 > len(icoData) {
			break
		}

		width := int(icoData[entryOffset])
		if width == 0 {
			width = 256
		}
		height := int(icoData[entryOffset+1])
		if height == 0 {
			height = 256
		}

		size := int(binary.LittleEndian.Uint32(icoData[entryOffset+8 : entryOffset+12]))
		offset := int(binary.LittleEndian.Uint32(icoData[entryOffset+12 : entryOffset+16]))

		if offset+size > len(icoData) {
			continue
		}

		// Create icon from this entry
		iconData := icoData[offset : offset+size]

		// For 16x16 (small icon)
		if width <= 16 && (smallBest == -1 || width > smallBest) {
			hIcon, _, _ := createIconFromResourceEx.Call(
				uintptr(unsafe.Pointer(&iconData[0])),
				uintptr(size),
				1,          // TRUE = icon
				0x00030000, // version
				16, 16,
				LR_DEFAULTCOLOR,
			)
			if hIcon != 0 {
				smallIcon = hIcon
				smallBest = width
			}
		}

		// For 32x32 (big icon)
		if width >= 32 && width <= 48 && (bigBest == -1 || (width >= 32 && width <= bigBest)) {
			hIcon, _, _ := createIconFromResourceEx.Call(
				uintptr(unsafe.Pointer(&iconData[0])),
				uintptr(size),
				1,          // TRUE = icon
				0x00030000, // version
				32, 32,
				LR_DEFAULTCOLOR,
			)
			if hIcon != 0 {
				bigIcon = hIcon
				bigBest = width
			}
		}
	}

	// Apply icons to window
	if smallIcon != 0 {
		sendMessageW.Call(hwnd, WM_SETICON, ICON_SMALL, smallIcon)
	}
	if bigIcon != 0 {
		sendMessageW.Call(hwnd, WM_SETICON, ICON_BIG, bigIcon)
	}
}

// FindMainWindow finds and stores the main window handle
func FindMainWindow() uintptr {
	if mainWindowHandle != 0 {
		return mainWindowHandle
	}
	titleW, _ := syscall.UTF16PtrFromString("e_n_u_f 2.0")
	hwnd, _, _ := findWindowW.Call(0, uintptr(unsafe.Pointer(titleW)))
	if hwnd != 0 {
		mainWindowHandle = hwnd
	}
	return mainWindowHandle
}

// HideMainWindow hides the window (minimize to tray)
func HideMainWindow() {
	hwnd := FindMainWindow()
	if hwnd != 0 {
		showWindow.Call(hwnd, SW_HIDE)
	}
}

// ShowMainWindow shows and restores the window
func ShowMainWindow() {
	hwnd := FindMainWindow()
	if hwnd != 0 {
		showWindow.Call(hwnd, SW_SHOW)
		showWindow.Call(hwnd, SW_RESTORE)
		setForegroundWnd.Call(hwnd)
	}
}

// IsMainWindowVisible checks if the window is visible
func IsMainWindowVisible() bool {
	hwnd := FindMainWindow()
	if hwnd == 0 {
		return false
	}
	ret, _, _ := isWindowVisible.Call(hwnd)
	return ret != 0
}

// IsMainWindowMinimized checks if the window is minimized
func IsMainWindowMinimized() bool {
	hwnd := FindMainWindow()
	if hwnd == 0 {
		return false
	}
	ret, _, _ := isIconic.Call(hwnd)
	return ret != 0
}
