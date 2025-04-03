package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"kb/client"
	"kb/server"
	"kb/types"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/go-vgo/robotgo"
)

const (
	PORT = 8080
)

var currentServer *server.Server
var currentClient *client.Client
var mainApp fyne.App // Keep reference to the app

// Map to keep track of open monitor windows per client address
var monitorWindows = make(map[string]fyne.Window)
var monitorWindowsMutex sync.Mutex

// getLocalMonitorInfo moved here from server/client as it uses common packages
func getLocalMonitorInfo() types.MonitorInfo {
	hostname, _ := os.Hostname()
	osStr := runtime.GOOS
	numDisplays := robotgo.DisplaysNum()
	screens := make([]types.ScreenRect, 0, numDisplays)
	for i := 0; i < numDisplays; i++ {
		x, y, w, h := robotgo.GetDisplayBounds(i)
		screens = append(screens, types.ScreenRect{ID: i, X: x, Y: y, W: w, H: h})
	}
	return types.MonitorInfo{
		Hostname: hostname,
		OS:       osStr,
		Screens:  screens,
	}
}

func main() {
	mainApp = app.New()
	window := mainApp.NewWindow("Keyboard Sharing")

	// Initialize client instance early for discovery
	currentClient = client.NewClient()

	window.SetContent(createMainContent(window))
	window.Resize(fyne.NewSize(400, 300))
	window.ShowAndRun()
}

// showMonitorManagementWindow creates and shows a new window displaying monitor info
func showMonitorManagementWindow(clientAddr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
	monitorWindowsMutex.Lock()
	defer monitorWindowsMutex.Unlock()

	// Close existing window for this client if open
	if win, ok := monitorWindows[clientAddr]; ok {
		win.Close()
	}

	monWin := mainApp.NewWindow(fmt.Sprintf("Monitor Management - %s", clientInfo.Hostname))

	serverLabel := widget.NewLabel(formatMonitorInfo("Server", serverInfo))
	serverLabel.Wrapping = fyne.TextWrapWord
	clientLabel := widget.NewLabel(formatMonitorInfo("Client: "+clientAddr, clientInfo))
	clientLabel.Wrapping = fyne.TextWrapWord

	// TODO: Add drag-and-drop layout canvas later
	layoutPlaceholder := widget.NewLabel("Drag-and-drop layout area (future)")

	content := container.NewVBox(
		widget.NewLabel("Monitor Configuration:"),
		widget.NewSeparator(),
		serverLabel,
		widget.NewSeparator(),
		clientLabel,
		widget.NewSeparator(),
		layoutPlaceholder,
	)

	monWin.SetContent(content)
	monWin.Resize(fyne.NewSize(500, 400))

	// Remove window from map when closed
	monWin.SetOnClosed(func() {
		monitorWindowsMutex.Lock()
		delete(monitorWindows, clientAddr)
		monitorWindowsMutex.Unlock()
	})

	monitorWindows[clientAddr] = monWin // Store reference
	monWin.Show()
}

// formatMonitorInfo helper function to create display string
func formatMonitorInfo(title string, info types.MonitorInfo) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s: %s (%s)\n", title, info.Hostname, info.OS))
	sb.WriteString(" Screens:\n")
	if len(info.Screens) == 0 {
		sb.WriteString("  (No screen data received)\n")
	} else {
		for _, s := range info.Screens {
			sb.WriteString(fmt.Sprintf("  - ID %d: %d x %d @ (%d, %d)\n", s.ID, s.W, s.H, s.X, s.Y))
		}
	}
	return sb.String()
}

// serverUIUpdater listens for client updates from the server and launches UI windows
func serverUIUpdater(serverInstance *server.Server) {
	if serverInstance == nil || serverInstance.ClientUpdateChan == nil {
		return
	}
	fmt.Println("Starting Server UI Updater...")
	serverMonitorInfo := getLocalMonitorInfo() // Get server info using local func

	for clientConn := range serverInstance.ClientUpdateChan {
		if clientConn != nil && clientConn.MonitorInfo != nil {
			clientAddr := clientConn.Conn.RemoteAddr().String()
			fmt.Printf("UI Updater: Received monitor info for %s\n", clientAddr)
			// Ensure UI updates run on the main thread (Fyne requirement)
			// Capture loop variables correctly for the goroutine
			clientInfoCopy := *clientConn.MonitorInfo
			go func(addr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
				showMonitorManagementWindow(addr, serverInfo, clientInfo) // Pass struct directly
			}(clientAddr, serverMonitorInfo, clientInfoCopy)
		}
	}
	fmt.Println("Server UI Updater finished.")
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

	// Start the UI updater goroutine
	go serverUIUpdater(currentServer)

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
	clientsLabel := widget.NewLabel("Waiting for clients...") // Placeholder for client count/list

	stopBtn := widget.NewButton("Stop Server", func() {
		if currentServer != nil {
			currentServer.Stop() // This will close ClientUpdateChan, stopping serverUIUpdater
			currentServer = nil
			// Close any open monitor windows
			monitorWindowsMutex.Lock()
			for _, win := range monitorWindows {
				win.Close()
			}
			monitorWindows = make(map[string]fyne.Window) // Clear map
			monitorWindowsMutex.Unlock()
			window.SetContent(createMainContent(window))
		}
	})

	content := container.NewVBox(
		statusLabel,
		deviceLabel,
		clientsLabel, // Add clients label
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

	// Data binding for the list of discovered servers
	discoveredServersBinding := binding.NewStringList()
	serverMap := make(map[string]types.DiscoveryMessage) // Use types.DiscoveryMessage

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
				newServerMap := make(map[string]types.DiscoveryMessage) // Use types.DiscoveryMessage
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
			// TODO: Show a message "Please select or enter a server address"
			return
		}

		// Stop discovery updates before connecting
		close(stopTickerChan)
		// No need to stop discovery listener here, Stop() handles it on success/back

		err := currentClient.Connect(serverAddr)
		if err != nil {
			// Show error temporarily, then return to connect screen
			errorLabel := widget.NewLabel(fmt.Sprintf("Error connecting: %v", err))
			backBtn := widget.NewButton("Back", func() {
				// Restart client discovery screen - creates new client, restarts listener
				startClient(window)
			})
			window.SetContent(container.NewVBox(errorLabel, backBtn))
			return
		}

		// Successful connection, show status screen
		showClientStatusScreen(window)
	})

	backBtn := widget.NewButton("Back", func() {
		close(stopTickerChan) // Stop the refresh ticker
		if currentClient != nil {
			currentClient.Stop() // Stop discovery listener & potentially running client
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
