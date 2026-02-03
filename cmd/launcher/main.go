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
	botProcess *exec.Cmd
	exeDir     string
	webviewRef webview2.WebView
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

	// Start systray in background goroutine
	go systray.Run(onTrayReady, onTrayExit)

	// Run webview on main thread - this blocks until window is destroyed
	runWebView()
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
		os.Exit(1)
		return
	}
	webviewRef = w

	// Set window icon from embedded data and start minimize watcher
	go func() {
		time.Sleep(500 * time.Millisecond)
		SetWindowIconFromMemory(iconData)

		// Watch for minimize - hide window to tray instead
		for {
			time.Sleep(100 * time.Millisecond)
			if IsMainWindowMinimized() {
				HideMainWindow()
			}
		}
	}()

	w.SetSize(1024, 768, webview2.HintNone)
	w.Navigate("http://localhost:24602")
	w.Run()

	// Window closed/destroyed - cleanup and exit completely
	cleanup()
	os.Exit(0)
}

func onTrayReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("e_n_u_f 2.0")
	systray.SetTooltip("e_n_u_f 2.0 - Twitch Markov Bot")

	mShow := systray.AddMenuItem("Show Window", "Bring window to front")
	mBrowser := systray.AddMenuItem("Open in Browser", "Open the web UI in your default browser")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Stop the bot and exit")

	go func() {
		for {
			select {
			case <-mShow.ClickedCh:
				ShowMainWindow()
			case <-mBrowser.ClickedCh:
				exec.Command("cmd", "/c", "start", "https://localhost:24601").Start()
			case <-mQuit.ClickedCh:
				if webviewRef != nil {
					webviewRef.Terminate()
				}
				return
			}
		}
	}()
}

func onTrayExit() {
	cleanup()
}

func cleanup() {
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
