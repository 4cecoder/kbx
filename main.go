package main

import (
	"fmt"

	"kb/client"
	"kb/server"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

const (
	PORT = 8080
)

var currentServer *server.Server
var currentClient *client.Client

func main() {
	myApp := app.New()
	window := myApp.NewWindow("Keyboard Sharing")

	// Create mode selection buttons
	serverBtn := widget.NewButton("Start Server", func() {
		startServer(window)
	})

	clientBtn := widget.NewButton("Start Client", func() {
		startClient(window)
	})

	// Create the main content
	content := container.NewVBox(
		widget.NewLabel("Keyboard Sharing App"),
		widget.NewLabel("Select mode:"),
		serverBtn,
		clientBtn,
	)

	window.SetContent(content)
	window.Resize(fyne.NewSize(300, 200))
	window.ShowAndRun()
}

func startServer(window fyne.Window) {
	if currentServer != nil {
		currentServer.Stop()
	}

	server, err := server.NewServer(PORT)
	if err != nil {
		window.SetContent(widget.NewLabel(fmt.Sprintf("Error starting server: %v", err)))
		return
	}

	currentServer = server
	server.Start()

	// Create server status content
	statusLabel := widget.NewLabel(fmt.Sprintf("Server running on port %d\nPress ESC to stop", PORT))
	stopBtn := widget.NewButton("Stop Server", func() {
		if currentServer != nil {
			currentServer.Stop()
			currentServer = nil
			window.SetContent(createMainContent(window))
		}
	})

	content := container.NewVBox(
		statusLabel,
		stopBtn,
	)
	window.SetContent(content)
}

func startClient(window fyne.Window) {
	if currentClient != nil {
		currentClient.Stop()
	}

	// Create connection dialog
	ipEntry := widget.NewEntry()
	ipEntry.SetPlaceHolder("Enter server IP (e.g., 192.168.1.100:8080)")

	connectBtn := widget.NewButton("Connect", func() {
		serverAddr := ipEntry.Text
		if serverAddr == "" {
			serverAddr = fmt.Sprintf("localhost:%d", PORT)
		}

		client, err := client.NewClient(serverAddr)
		if err != nil {
			window.SetContent(widget.NewLabel(fmt.Sprintf("Error connecting to server: %v", err)))
			return
		}

		currentClient = client
		client.Start()

		// Create client status content
		statusLabel := widget.NewLabel("Connected to server\nPress ESC to disconnect")
		stopBtn := widget.NewButton("Disconnect", func() {
			if currentClient != nil {
				currentClient.Stop()
				currentClient = nil
				window.SetContent(createMainContent(window))
			}
		})

		content := container.NewVBox(
			statusLabel,
			stopBtn,
		)
		window.SetContent(content)
	})

	content := container.NewVBox(
		widget.NewLabel("Enter server address:"),
		ipEntry,
		connectBtn,
	)
	window.SetContent(content)
}

func createMainContent(window fyne.Window) fyne.CanvasObject {
	serverBtn := widget.NewButton("Start Server", func() {
		startServer(window)
	})

	clientBtn := widget.NewButton("Start Client", func() {
		startClient(window)
	})

	return container.NewVBox(
		widget.NewLabel("Keyboard Sharing App"),
		widget.NewLabel("Select mode:"),
		serverBtn,
		clientBtn,
	)
}
