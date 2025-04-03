package ui

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"time"

	"kb/client"
	"kb/server"
	"kb/types"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/widget"
	"github.com/go-vgo/robotgo"
)

const (
	PORT = 8080
)

// VirtualScreen represents a screen in the combined virtual layout
type VirtualScreen struct {
	ID       int              `json:"id"`
	Hostname string           `json:"hostname"`
	IsServer bool             `json:"is_server"`
	Original types.ScreenRect `json:"original"` // Original dimensions/coords
	Widget   *monitorWidget   `json:"-"`        // Reference to the widget
	Position fyne.Position    `json:"position"` // Current position in the layout window (scaled)
	Size     fyne.Size        `json:"size"`     // Current size in the layout window (scaled)
}

// AppUI holds the state and components of the application UI
type AppUI struct {
	fyneApp fyne.App
	mainWin fyne.Window

	client *client.Client
	server *server.Server

	monitorWindows    map[string]fyne.Window
	monitorWindowsMtx sync.Mutex
	virtualLayouts    map[string]map[string]*VirtualScreen
	virtualLayoutsMtx sync.Mutex
}

// NewAppUI creates a new UI application instance
func NewAppUI(app fyne.App, cli *client.Client) *AppUI {
	return &AppUI{
		fyneApp:        app,
		client:         cli,
		server:         nil,
		monitorWindows: make(map[string]fyne.Window),
		virtualLayouts: make(map[string]map[string]*VirtualScreen),
	}
}

// Run starts the application UI
func (a *AppUI) Run() {
	a.mainWin = a.fyneApp.NewWindow("Keyboard Sharing")
	a.mainWin.SetContent(a.createMainContent())
	a.mainWin.Resize(fyne.NewSize(400, 300))
	a.mainWin.ShowAndRun()
}

// Helper to create a unique key for a screen
func screenKey(hostname string, screenID int) string {
	return fmt.Sprintf("%s-%d", hostname, screenID)
}

// getLocalMonitorInfo helper
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

// --- UI Creation Methods ---

// createMainContent builds the initial main menu screen
func (a *AppUI) createMainContent() fyne.CanvasObject {
	serverBtn := widget.NewButton("Start Server", func() {
		a.startServerUI()
	})

	clientBtn := widget.NewButton("Start Client", func() {
		a.startClientUI()
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

// startServerUI handles the logic when the "Start Server" button is clicked
func (a *AppUI) startServerUI() {
	if a.client != nil {
		a.client.Stop()
	}
	if a.server != nil {
		a.server.Stop()
	}

	serverInstance, err := server.NewServer(PORT)
	if err != nil {
		log.Printf("Error starting server: %v", err)
		a.mainWin.SetContent(container.NewVBox(
			widget.NewLabel(fmt.Sprintf("Error starting server: %v", err)),
			widget.NewButton("Back", func() {
				a.mainWin.SetContent(a.createMainContent())
			}),
		))
		return
	}

	a.server = serverInstance
	a.server.Start()

	go a.serverUIUpdater() // Start the listener for client monitor info

	serverIP := a.server.GetListenIP()
	serverPort := a.server.GetListenPort()
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
	clientsLabel := widget.NewLabel("Waiting for clients...")

	stopBtn := widget.NewButton("Stop Server", func() {
		if a.server != nil {
			a.server.Stop()
			a.server = nil
			// Close any open monitor windows
			a.monitorWindowsMtx.Lock()
			for _, win := range a.monitorWindows {
				win.Close()
			}
			a.monitorWindows = make(map[string]fyne.Window) // Clear map
			a.monitorWindowsMtx.Unlock()
			a.virtualLayoutsMtx.Lock()
			a.virtualLayouts = make(map[string]map[string]*VirtualScreen)
			a.virtualLayoutsMtx.Unlock()
			a.mainWin.SetContent(a.createMainContent())
		}
	})

	content := container.NewVBox(
		statusLabel,
		deviceLabel,
		clientsLabel,
		widget.NewLabel("Press ESC to stop (or use button)"),
		stopBtn,
	)
	a.mainWin.SetContent(content)
}

// startClientUI handles the logic when the "Start Client" button is clicked
func (a *AppUI) startClientUI() {
	if a.server != nil {
		a.server.Stop()
		a.server = nil
	}
	if a.client != nil {
		a.client.Stop()
	}
	a.client = client.NewClient() // Create a fresh client instance for discovery

	discoveredServersBinding := binding.NewStringList()
	serverMap := make(map[string]types.DiscoveryMessage)

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
	go a.client.StartDiscoveryListener()

	refreshTicker := time.NewTicker(3 * time.Second)

	go func() {
		defer refreshTicker.Stop()
		for {
			select {
			case <-refreshTicker.C:
				found := a.client.GetFoundServers()
				servers := []string{}
				newServerMap := make(map[string]types.DiscoveryMessage)
				for _, info := range found {
					displayStr := fmt.Sprintf("%s (%s - %s)", info.Hostname, info.ServerIP, info.OS)
					servers = append(servers, displayStr)
					newServerMap[displayStr] = info
				}
				discoveredServersBinding.Set(servers)
				serverMap = newServerMap
			case <-stopTickerChan:
				log.Println("Stopping discovery refresh ticker.")
				return
			}
		}
	}()

	connectBtn := widget.NewButton("Connect", func() {
		serverAddr := ipEntry.Text
		if serverAddr == "" {
			// TODO: Show a message
			return
		}

		close(stopTickerChan)

		err := a.client.Connect(serverAddr)
		if err != nil {
			log.Printf("Error connecting: %v", err)
			errorLabel := widget.NewLabel(fmt.Sprintf("Error connecting: %v", err))
			backBtn := widget.NewButton("Back", func() {
				a.startClientUI() // Go back to client discovery UI
			})
			a.mainWin.SetContent(container.NewVBox(errorLabel, backBtn))
			return
		}
		a.showClientStatusScreen()
	})

	backBtn := widget.NewButton("Back", func() {
		close(stopTickerChan)
		if a.client != nil {
			a.client.Stop()
		}
		a.mainWin.SetContent(a.createMainContent())
	})

	content := container.NewVBox(
		widget.NewLabel("Available Servers (select or enter manually):"),
		serverList,
		ipEntry,
		container.NewHBox(backBtn, connectBtn),
	)

	a.mainWin.SetContent(content)
}

// showClientStatusScreen displays the screen after successfully connecting as a client
func (a *AppUI) showClientStatusScreen() {
	if a.client == nil {
		a.mainWin.SetContent(a.createMainContent())
		return
	}
	serverInfo := a.client.GetConnectedServerInfo()
	localHostname, _ := os.Hostname()
	localOs := runtime.GOOS

	statusText := fmt.Sprintf("Connected to: %s (%s)", serverInfo.Hostname, serverInfo.ServerIP)
	localInfoText := fmt.Sprintf("Local: %s (%s)", localHostname, localOs)
	remoteOsText := fmt.Sprintf("Remote OS: %s", serverInfo.OS)

	statusLabel := widget.NewLabel(statusText)
	localLabel := widget.NewLabel(localInfoText)
	remoteOsLabel := widget.NewLabel(remoteOsText)

	stopBtn := widget.NewButton("Disconnect", func() {
		if a.client != nil {
			a.client.Stop()
			a.mainWin.SetContent(a.createMainContent())
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
	a.mainWin.SetContent(content)
}

// showMonitorManagementWindow creates and shows a new window displaying draggable monitors
func (a *AppUI) showMonitorManagementWindow(clientAddr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
	a.monitorWindowsMtx.Lock()
	if win, ok := a.monitorWindows[clientAddr]; ok {
		win.Close()
	}
	a.monitorWindowsMtx.Unlock()

	monWin := a.fyneApp.NewWindow(fmt.Sprintf("Monitor Arrangement - %s", clientInfo.Hostname))

	layoutCanvas := container.NewWithoutLayout()
	currentLayout := make(map[string]*VirtualScreen)

	scale := float32(0.1)
	padding := float32(20)
	minX, minY, maxX, maxY := float32(0), float32(0), float32(0), float32(0)
	// Initial bounds check needs at least one screen
	if len(serverInfo.Screens) > 0 {
		minX, minY = float32(serverInfo.Screens[0].X), float32(serverInfo.Screens[0].Y)
		maxX, maxY = float32(serverInfo.Screens[0].X+serverInfo.Screens[0].W), float32(serverInfo.Screens[0].Y+serverInfo.Screens[0].H)
	} else if len(clientInfo.Screens) > 0 { // Use client if server has none
		minX, minY = float32(clientInfo.Screens[0].X), float32(clientInfo.Screens[0].Y)
		maxX, maxY = float32(clientInfo.Screens[0].X+clientInfo.Screens[0].W), float32(clientInfo.Screens[0].Y+clientInfo.Screens[0].H)
	} else {
		// No screens reported? Handle error or show empty window
		log.Println("Warning: No screen info available for monitor management window for", clientAddr)
		monWin.SetContent(widget.NewLabel("No screen information received from server or client."))
		monWin.Resize(fyne.NewSize(300, 100))
		monWin.Show()
		return
	}

	allScreens := append([]types.ScreenRect{}, serverInfo.Screens...)
	allScreens = append(allScreens, clientInfo.Screens...)

	for _, screen := range allScreens {
		if float32(screen.X) < minX {
			minX = float32(screen.X)
		}
		if float32(screen.Y) < minY {
			minY = float32(screen.Y)
		}
		if float32(screen.X+screen.W) > maxX {
			maxX = float32(screen.X + screen.W)
		}
		if float32(screen.Y+screen.H) > maxY {
			maxY = float32(screen.Y + screen.H)
		}
	}

	offsetX := -minX*scale + padding
	offsetY := -minY*scale + padding

	addMonitor := func(info types.MonitorInfo, isServer bool) {
		for _, screen := range info.Screens {
			mw := NewMonitorWidget(screen, info.Hostname, isServer)

			scaledW := float32(screen.W) * scale
			scaledH := float32(screen.H) * scale
			widgetX := (float32(screen.X)*scale + offsetX)
			widgetY := (float32(screen.Y)*scale + offsetY)
			initialPos := fyne.NewPos(widgetX, widgetY)
			initialSize := fyne.NewSize(scaledW, scaledH)

			mw.Resize(initialSize)
			mw.SetOffset(initialPos) // Use SetOffset which also Moves

			layoutCanvas.Add(mw)

			key := screenKey(info.Hostname, screen.ID)
			vScreen := &VirtualScreen{
				ID:       screen.ID,
				Hostname: info.Hostname,
				IsServer: isServer,
				Original: screen,
				Widget:   mw,
				Position: initialPos,
				Size:     initialSize,
			}
			currentLayout[key] = vScreen

			mw.OnDragEnd = func(draggedWidget *monitorWidget) {
				finalOffset := draggedWidget.GetOffset()
				currentKey := screenKey(draggedWidget.hostname, draggedWidget.screenInfo.ID)
				log.Printf("Callback DragEnd %s to %v\n", currentKey, finalOffset)
				a.virtualLayoutsMtx.Lock()
				if clientLayout, ok := a.virtualLayouts[clientAddr]; ok {
					if vScreen, ok := clientLayout[currentKey]; ok {
						vScreen.Position = finalOffset // Update position in shared map
					}
				} else {
					// Initialize map if it doesn't exist (shouldn't happen often here)
					a.virtualLayouts[clientAddr] = make(map[string]*VirtualScreen)
					// Try updating again? Or log error?
				}
				a.virtualLayoutsMtx.Unlock()
				// TODO: Snapping logic
				// TODO: Save layout persistently?
			}
		}
	}

	addMonitor(serverInfo, true)
	addMonitor(clientInfo, false)

	a.virtualLayoutsMtx.Lock()
	a.virtualLayouts[clientAddr] = currentLayout // Store initial layout
	a.virtualLayoutsMtx.Unlock()

	content := layoutCanvas

	totalWidth := (maxX-minX)*scale + 2*padding
	totalHeight := (maxY-minY)*scale + 2*padding
	monWin.SetContent(content)
	monWin.Resize(fyne.NewSize(totalWidth, totalHeight))

	monWin.SetOnClosed(func() {
		a.monitorWindowsMtx.Lock()
		delete(a.monitorWindows, clientAddr)
		a.monitorWindowsMtx.Unlock()
		a.virtualLayoutsMtx.Lock()
		delete(a.virtualLayouts, clientAddr)
		a.virtualLayoutsMtx.Unlock()
	})

	a.monitorWindowsMtx.Lock()
	a.monitorWindows[clientAddr] = monWin
	a.monitorWindowsMtx.Unlock()
	monWin.Show()
}

// serverUIUpdater listens for client updates from the server and launches UI windows
func (a *AppUI) serverUIUpdater() {
	if a.server == nil || a.server.ClientUpdateChan == nil {
		log.Println("Server or ClientUpdateChan is nil, cannot start UI updater.")
		return
	}
	log.Println("Starting Server UI Updater...")
	serverMonitorInfo := getLocalMonitorInfo()

	for clientConn := range a.server.ClientUpdateChan {
		if clientConn != nil && clientConn.MonitorInfo != nil {
			clientAddr := clientConn.Conn.RemoteAddr().String()
			log.Printf("UI Updater: Received monitor info for %s\n", clientAddr)
			clientInfoCopy := *clientConn.MonitorInfo
			// Launch window creation in a goroutine to avoid blocking the update channel reader
			// Note: Fyne UI operations *should* ideally happen on the main thread.
			// If creating windows from goroutines causes issues (crashes, visual glitches),
			// this needs refactoring to use fyneApp.SendNotification or a channel back to the main loop.
			go func(addr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
				a.showMonitorManagementWindow(addr, serverInfo, clientInfo)
			}(clientAddr, serverMonitorInfo, clientInfoCopy)
		}
	}
	log.Println("Server UI Updater finished.")
}
