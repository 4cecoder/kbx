package ui

import (
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
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
}

// NewAppUI creates a new UI application instance
func NewAppUI(app fyne.App, cli *client.Client) *AppUI {
	return &AppUI{
		fyneApp:        app,
		client:         cli,
		server:         nil,
		monitorWindows: make(map[string]fyne.Window),
	}
}

// Run starts the application UI
func (a *AppUI) Run() {
	a.mainWin = a.fyneApp.NewWindow("Keyboard Sharing")
	a.mainWin.SetContent(a.createMainContent())
	a.mainWin.Resize(fyne.NewSize(600, 400))
	a.mainWin.ShowAndRun()
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
		a.client = nil
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

	// Start listening for server warnings (e.g., macOS permissions)
	go func() {
		warningChan := a.server.GetWarningChan()
		select {
		case warningMsg := <-warningChan:
			log.Printf("Received server warning: %s", warningMsg)
			if a.server != nil { // Check if server is still running
				warningLabel := widget.NewLabelWithStyle(
					fmt.Sprintf(
						"WARNING: %s\nPlease grant Accessibility permission to **this application** in System Settings -> Privacy & Security, then restart using the button below.",
						warningMsg,
					),
					fyne.TextAlignLeading,
					fyne.TextStyle{Bold: true},
				)
				warningLabel.Wrapping = fyne.TextWrapWord

				restartBtn := widget.NewButton("Restart App Now", func() {
					log.Println("Restart button clicked. Attempting to restart application...")

					// Attempt to stop server gracefully
					if a.server != nil {
						log.Println("Stopping current server instance...")
						a.server.Stop()
						a.server = nil
					}
					// Stop client too, just in case (though unlikely in server mode)
					if a.client != nil {
						a.client.Stop()
						a.client = nil
					}

					executable, err := os.Executable()
					if err != nil {
						log.Printf("Error getting executable path: %v", err)
						// Optionally show an error dialog to the user here
						return
					}

					cmd := exec.Command(executable, os.Args[1:]...)
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr

					err = cmd.Start() // Start new process
					if err != nil {
						log.Printf("Error starting new process: %v", err)
						// Optionally show an error dialog
						return
					}

					log.Println("New process started, quitting current application.")
					a.fyneApp.Quit() // Quit the current Fyne app instance
				})

				// Prepend warning and button to the existing content
				if currentContent, ok := a.mainWin.Content().(*fyne.Container); ok {
					// Add a little space between warning and button
					warningContainer := container.NewVBox(warningLabel, widget.NewSeparator(), restartBtn)
					currentContent.Objects = append([]fyne.CanvasObject{warningContainer, widget.NewSeparator()}, currentContent.Objects...)
					currentContent.Refresh()
				}
			}
		case <-time.After(1 * time.Second): // Timeout if no immediate warning
			// No warning received shortly after start, assume okay for now
			return
		}
	}()
}

// startClientUI handles the logic when the "Start Client" button is clicked
func (a *AppUI) startClientUI() {
	if a.server != nil {
		a.server.Stop()
		a.server = nil
	}
	if a.client != nil {
		a.client.Stop()
		a.client = nil
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
			a.client = nil
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
			a.client = nil
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

// showMonitorManagementWindow requires AppUI to access the server
func (a *AppUI) showMonitorManagementWindow(clientAddr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
	a.monitorWindowsMtx.Lock()
	if win, ok := a.monitorWindows[clientAddr]; ok {
		win.Close()
	}
	a.monitorWindowsMtx.Unlock()
	monWin := a.fyneApp.NewWindow(fmt.Sprintf("Monitor Arrangement - %s", clientInfo.Hostname))

	layoutCanvas := container.NewWithoutLayout()
	// Store layout locally for this window instance, but update server on drag end
	windowLayout := make(map[string]*types.VirtualScreen)

	scale := float32(0.1)
	padding := float32(20)
	minX, minY, maxX, maxY := float32(0), float32(0), float32(0), float32(0)
	if len(serverInfo.Screens) > 0 {
		minX, minY = float32(serverInfo.Screens[0].X), float32(serverInfo.Screens[0].Y)
		maxX, maxY = float32(serverInfo.Screens[0].X+serverInfo.Screens[0].W), float32(serverInfo.Screens[0].Y+serverInfo.Screens[0].H)
	} else if len(clientInfo.Screens) > 0 {
		minX, minY = float32(clientInfo.Screens[0].X), float32(clientInfo.Screens[0].Y)
		maxX, maxY = float32(clientInfo.Screens[0].X+clientInfo.Screens[0].W), float32(clientInfo.Screens[0].Y+clientInfo.Screens[0].H)
	} else {
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
			mw.SetOffset(initialPos)
			layoutCanvas.Add(mw)

			key := types.ScreenKey(info.Hostname, screen.ID)
			vScreen := &types.VirtualScreen{
				ID:       screen.ID,
				Hostname: info.Hostname,
				IsServer: isServer,
				Original: screen,
				Position: types.Position{X: initialPos.X, Y: initialPos.Y},
				Size:     types.Size{Width: initialSize.Width, Height: initialSize.Height},
			}
			windowLayout[key] = vScreen

			mw.OnDragEnd = func(draggedWidget *monitorWidget) {
				finalOffset := draggedWidget.GetOffset()
				currentKey := types.ScreenKey(draggedWidget.hostname, draggedWidget.screenInfo.ID)
				log.Printf("Callback DragEnd %s to %v\n", currentKey, finalOffset)

				// Update the local window layout first
				if vScreen, ok := windowLayout[currentKey]; ok {
					vScreen.Position = types.Position{X: finalOffset.X, Y: finalOffset.Y}
				}

				// Calculate the abstract layout config based on visual arrangement
				layoutConfig := calculateLayoutConfiguration(windowLayout)

				// Now, update the server's master layout for this client
				if a.server != nil {
					a.server.UpdateLayout(clientAddr, layoutConfig)
				} else {
					log.Println("Error: Server is nil, cannot update layout.")
				}
				// TODO: Snapping logic
			}
		}
	}

	addMonitor(serverInfo, true)
	addMonitor(clientInfo, false)

	// Update the server's master layout with the initial state
	if a.server != nil {
		initialLayoutConfig := calculateLayoutConfiguration(windowLayout)
		a.server.UpdateLayout(clientAddr, initialLayoutConfig)
	}

	content := layoutCanvas
	totalWidth := (maxX-minX)*scale + 2*padding
	totalHeight := (maxY-minY)*scale + 2*padding
	monWin.SetContent(content)
	monWin.Resize(fyne.NewSize(totalWidth, totalHeight))
	monWin.SetOnClosed(func() {
		a.monitorWindowsMtx.Lock()
		delete(a.monitorWindows, clientAddr)
		a.monitorWindowsMtx.Unlock()
		// Don't delete server layout here, it should persist until client disconnects
	})
	a.monitorWindowsMtx.Lock()
	a.monitorWindows[clientAddr] = monWin
	a.monitorWindowsMtx.Unlock()
	monWin.Show()
}

// calculateLayoutConfiguration determines edge links based on the visual arrangement
func calculateLayoutConfiguration(layout map[string]*types.VirtualScreen) *types.LayoutConfiguration {
	const snapThreshold = 15.0 // Max distance between edges to consider them linked

	var links []types.EdgeLink

	// Convert map to slice for easier iteration
	screens := make([]*types.VirtualScreen, 0, len(layout))
	for _, v := range layout {
		screens = append(screens, v)
	}

	// Compare every screen with every other screen
	for i := 0; i < len(screens); i++ {
		s1 := screens[i]
		pos1 := s1.Position // Use UI position/size
		size1 := s1.Size

		for j := i + 1; j < len(screens); j++ {
			s2 := screens[j]
			pos2 := s2.Position
			size2 := s2.Size

			// Check Right edge of s1 to Left edge of s2
			verticalOverlap := math.Max(float64(pos1.Y), float64(pos2.Y)) < math.Min(float64(pos1.Y+size1.Height), float64(pos2.Y+size2.Height))
			horizontalDistance := math.Abs(float64((pos1.X + size1.Width) - pos2.X))
			if verticalOverlap && horizontalDistance <= float64(snapThreshold) {
				links = append(links, types.EdgeLink{
					FromHostname: s1.Hostname, FromScreenID: s1.Original.ID, FromEdge: types.EdgeRight,
					ToHostname: s2.Hostname, ToScreenID: s2.Original.ID, ToEdge: types.EdgeLeft,
				})
				// Add reciprocal link
				links = append(links, types.EdgeLink{
					FromHostname: s2.Hostname, FromScreenID: s2.Original.ID, FromEdge: types.EdgeLeft,
					ToHostname: s1.Hostname, ToScreenID: s1.Original.ID, ToEdge: types.EdgeRight,
				})
			}

			// Check Left edge of s1 to Right edge of s2
			horizontalDistance = math.Abs(float64(pos1.X - (pos2.X + size2.Width)))
			if verticalOverlap && horizontalDistance <= float64(snapThreshold) {
				links = append(links, types.EdgeLink{
					FromHostname: s1.Hostname, FromScreenID: s1.Original.ID, FromEdge: types.EdgeLeft,
					ToHostname: s2.Hostname, ToScreenID: s2.Original.ID, ToEdge: types.EdgeRight,
				})
				links = append(links, types.EdgeLink{
					FromHostname: s2.Hostname, FromScreenID: s2.Original.ID, FromEdge: types.EdgeRight,
					ToHostname: s1.Hostname, ToScreenID: s1.Original.ID, ToEdge: types.EdgeLeft,
				})
			}

			// Check Bottom edge of s1 to Top edge of s2
			horizontalOverlap := math.Max(float64(pos1.X), float64(pos2.X)) < math.Min(float64(pos1.X+size1.Width), float64(pos2.X+size2.Width))
			verticalDistance := math.Abs(float64((pos1.Y + size1.Height) - pos2.Y))
			if horizontalOverlap && verticalDistance <= float64(snapThreshold) {
				links = append(links, types.EdgeLink{
					FromHostname: s1.Hostname, FromScreenID: s1.Original.ID, FromEdge: types.EdgeBottom,
					ToHostname: s2.Hostname, ToScreenID: s2.Original.ID, ToEdge: types.EdgeTop,
				})
				links = append(links, types.EdgeLink{
					FromHostname: s2.Hostname, FromScreenID: s2.Original.ID, FromEdge: types.EdgeTop,
					ToHostname: s1.Hostname, ToScreenID: s1.Original.ID, ToEdge: types.EdgeBottom,
				})
			}

			// Check Top edge of s1 to Bottom edge of s2
			verticalDistance = math.Abs(float64(pos1.Y - (pos2.Y + size2.Height)))
			if horizontalOverlap && verticalDistance <= float64(snapThreshold) {
				links = append(links, types.EdgeLink{
					FromHostname: s1.Hostname, FromScreenID: s1.Original.ID, FromEdge: types.EdgeTop,
					ToHostname: s2.Hostname, ToScreenID: s2.Original.ID, ToEdge: types.EdgeBottom,
				})
				links = append(links, types.EdgeLink{
					FromHostname: s2.Hostname, FromScreenID: s2.Original.ID, FromEdge: types.EdgeBottom,
					ToHostname: s1.Hostname, ToScreenID: s1.Original.ID, ToEdge: types.EdgeTop,
				})
			}
		}
	}

	log.Printf("Calculated %d edge links from layout", len(links))
	return &types.LayoutConfiguration{Links: links}
}

// serverUIUpdater passes the AppUI instance to showMonitorManagementWindow
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
			// Pass AppUI instance 'a' so the window can call a.server.UpdateLayout
			go func(appUI *AppUI, addr string, serverInfo types.MonitorInfo, clientInfo types.MonitorInfo) {
				appUI.showMonitorManagementWindow(addr, serverInfo, clientInfo)
			}(a, clientAddr, serverMonitorInfo, clientInfoCopy)
		}
	}
	log.Println("Server UI Updater finished.")
}
