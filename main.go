package main

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"kb/client"
	"kb/server"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
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

	// Initialize client instance early for discovery
	currentClient = client.NewClient()

	window.SetContent(createMainContent(window))
	window.Resize(fyne.NewSize(400, 300))
	window.ShowAndRun()
}

func startServer(window fyne.Window) {
	if currentServer != nil {
		currentServer.Stop()
	}
	if currentClient != nil {
		currentClient.Stop()
	}

	server, err := server.NewServer(PORT)
	if err != nil {
		window.SetContent(container.NewVBox(
			widget.NewLabel(fmt.Sprintf("Error starting server: %v", err)),
			widget.NewButton("Back", func() {
				window.SetContent(createMainContent(window))
			}),
		))
		return
	}

	currentServer = server
	server.Start()

	serverIP := currentServer.GetListenIP()
	serverPort := currentServer.GetListenPort()
	localHostname, _ := os.Hostname()
	localOs := runtime.GOOS

	var statusText string
	if serverPort != -1 {
		statusText = fmt.Sprintf("Server running at %s:%d", serverIP, serverPort)
	} else {
		statusText = fmt.Sprintf("Server running (Error getting port)")
	}
	deviceInfoText := fmt.Sprintf("Hostname: %s, OS: %s", localHostname, localOs)

	statusLabel := widget.NewLabel(statusText)
	deviceLabel := widget.NewLabel(deviceInfoText)

	stopBtn := widget.NewButton("Stop Server", func() {
		if currentServer != nil {
			currentServer.Stop()
			currentServer = nil
			window.SetContent(createMainContent(window))
		}
	})

	content := container.NewVBox(
		statusLabel,
		deviceLabel,
		widget.NewLabel("Press ESC to stop (or use button)"),
		stopBtn,
	)
	window.SetContent(content)
}

func startClient(window fyne.Window) {
	if currentServer != nil {
		currentServer.Stop()
		currentServer = nil
	}
	if currentClient != nil {
		currentClient.Stop()
	}
	currentClient = client.NewClient()

	discoveredServersBinding := binding.NewStringList()
	serverMap := make(map[string]client.DiscoveryMessage)

	serverList := widget.NewListWithData(
		discoveredServersBinding,
		func() fyne.CanvasObject {
			return widget.NewLabel("Template Server")
		},
		func(item binding.DataItem, obj fyne.CanvasObject) {
			label := obj.(*widget.Label)
			strItem := item.(binding.String)
			str, _ := strItem.Get()
			label.SetText(str)
		},
	)

	ipEntry := widget.NewEntry()
	ipEntry.SetPlaceHolder("Select from list or enter manually (e.g., 192.168.1.100:8080)")

	serverList.OnSelected = func(id widget.ListItemID) {
		servers, _ := discoveredServersBinding.Get()
		if id < len(servers) {
			selectedServerStr := servers[id]
			if serverInfo, ok := serverMap[selectedServerStr]; ok {
				addr := fmt.Sprintf("%s:%d", serverInfo.ServerIP, serverInfo.Port)
				ipEntry.SetText(addr)
			}
		}
	}

	stopTickerChan := make(chan struct{})
	go currentClient.StartDiscoveryListener()

	refreshTicker := time.NewTicker(3 * time.Second)

	go func() {
		defer refreshTicker.Stop()
		for {
			select {
			case <-refreshTicker.C:
				found := currentClient.GetFoundServers()
				servers := []string{}
				newServerMap := make(map[string]client.DiscoveryMessage)
				for _, info := range found {
					displayStr := fmt.Sprintf("%s (%s - %s)", info.Hostname, info.ServerIP, info.OS)
					servers = append(servers, displayStr)
					newServerMap[displayStr] = info
				}
				discoveredServersBinding.Set(servers)
				serverMap = newServerMap
			case <-stopTickerChan:
				fmt.Println("Stopping discovery refresh ticker.")
				return
			}
		}
	}()

	connectBtn := widget.NewButton("Connect", func() {
		serverAddr := ipEntry.Text
		if serverAddr == "" {
			return
		}

		close(stopTickerChan)

		err := currentClient.Connect(serverAddr)
		if err != nil {
			errorLabel := widget.NewLabel(fmt.Sprintf("Error connecting: %v", err))
			backBtn := widget.NewButton("Back", func() {
				startClient(window)
			})
			window.SetContent(container.NewVBox(errorLabel, backBtn))
			return
		}

		showClientStatusScreen(window)
	})

	backBtn := widget.NewButton("Back", func() {
		close(stopTickerChan)
		if currentClient != nil {
			currentClient.Stop()
		}
		window.SetContent(createMainContent(window))
	})

	content := container.NewVBox(
		widget.NewLabel("Available Servers (select or enter manually):"),
		serverList,
		ipEntry,
		container.NewHBox(backBtn, connectBtn),
	)

	window.SetContent(content)
}

func showClientStatusScreen(window fyne.Window) {
	if currentClient == nil {
		window.SetContent(createMainContent(window))
		return
	}
	serverInfo := currentClient.GetConnectedServerInfo()
	localHostname, _ := os.Hostname()
	localOs := runtime.GOOS

	statusText := fmt.Sprintf("Connected to: %s (%s)", serverInfo.Hostname, serverInfo.ServerIP)
	localInfoText := fmt.Sprintf("Local: %s (%s)", localHostname, localOs)
	remoteOsText := fmt.Sprintf("Remote OS: %s", serverInfo.OS)

	statusLabel := widget.NewLabel(statusText)
	localLabel := widget.NewLabel(localInfoText)
	remoteOsLabel := widget.NewLabel(remoteOsText)

	stopBtn := widget.NewButton("Disconnect", func() {
		if currentClient != nil {
			currentClient.Stop()
			window.SetContent(createMainContent(window))
		}
	})

	content := container.NewVBox(
		statusLabel,
		remoteOsLabel,
		widget.NewSeparator(),
		localLabel,
		widget.NewLabel("Press ESC to disconnect (or use button)"),
		stopBtn,
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

	localHostname, _ := os.Hostname()
	localOs := runtime.GOOS
	deviceInfoLabel := widget.NewLabel(fmt.Sprintf("Local: %s (%s)", localHostname, localOs))

	return container.NewVBox(
		widget.NewLabel("Keyboard Sharing App"),
		widget.NewSeparator(),
		deviceInfoLabel,
		widget.NewSeparator(),
		widget.NewLabel("Select mode:"),
		serverBtn,
		clientBtn,
	)
}
