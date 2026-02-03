//go:build windows

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/getlantern/systray"
	"github.com/jchv/go-webview2"
)

//go:embed icon.ico
var iconData []byte

var (
	botProcess   *exec.Cmd
	webview      webview2.WebView
	exeDir       string
	windowClosed bool
)

func main() {
	// Get the directory where the launcher is located
	exePath, err := os.Executable()
	if err != nil {
		showError("Failed to get executable path: " + err.Error())
		return
	}
	exeDir = filepath.Dir(exePath)

	// Start the bot process
	botPath := filepath.Join(exeDir, "twitchbot.exe")
	if _, err := os.Stat(botPath); os.IsNotExist(err) {
		showError("twitchbot.exe not found in " + exeDir)
		return
	}

	botProcess = exec.Command(botPath)
	botProcess.Dir = exeDir
	hideWindow(botProcess)

	if err := botProcess.Start(); err != nil {
		showError("Failed to start bot: " + err.Error())
		return
	}

	// Wait for the web server to be ready
	time.Sleep(3 * time.Second)

	// Start systray (this runs the main loop)
	systray.Run(onReady, onExit)
}

func runWebView() {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "e_n_u_f 2.0",
			Width:  1024,
			Height: 768,
			IconId: 0,
			Center: true,
		},
	})
	if w == nil {
		showError("Failed to create webview. Make sure Microsoft Edge WebView2 Runtime is installed.")
		cleanup()
		return
	}
	webview = w

	// Set window icon from embedded data
	go func() {
		time.Sleep(500 * time.Millisecond)
		SetWindowIconFromMemory(iconData)
	}()

	w.SetSize(1024, 768, webview2.HintNone)
	w.Navigate("http://localhost:24602") // Use HTTP port (no cert warning)
	w.Run()

	// Window was closed - hide to tray instead of quitting
	webview = nil
	windowClosed = true
}

func onReady() {
	systray.SetIcon(getIcon())
	systray.SetTitle("e_n_u_f 2.0")
	systray.SetTooltip("e_n_u_f 2.0 - Twitch Markov Bot")

	mShow := systray.AddMenuItem("Show Window", "Open the main window")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop the bot and exit")

	// Open the window initially
	go runWebView()

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				// If window was closed, create a new one
				if windowClosed || webview == nil {
					windowClosed = false
					go runWebView()
				} else {
					// Try to show existing window
					ShowMainWindow()
				}
			case <-mQuit.ClickedCh:
				cleanup()
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	cleanup()
}

func cleanup() {
	if webview != nil {
		webview.Destroy()
		webview = nil
	}
	if botProcess != nil && botProcess.Process != nil {
		botProcess.Process.Kill()
	}
}

func showError(msg string) {
	exec.Command("powershell", "-Command", fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.MessageBox]::Show('%s', 'e_n_u_f 2.0 Error', 'OK', 'Error')`, msg)).Run()
	f, _ := os.OpenFile(filepath.Join(exeDir, "launcher_error.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if f != nil {
		f.WriteString(fmt.Sprintf("%s: %s\n", time.Now().Format(time.RFC3339), msg))
		f.Close()
	}
}

func getIcon() []byte {
	return iconData
}
