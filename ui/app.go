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
	"fyne.io/fyne/v2/theme"
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

	// Create buttons with icons for screen switching
	switchToClientBtn := widget.NewButtonWithIcon("Switch to Client", theme.LogoutIcon(), func() {
		// Find first available client
		a.server.RLockClientsMutex()
		clients := a.server.GetClients()
		if len(clients) == 0 {
			// Show a message if no clients
			fyne.CurrentApp().SendNotification(&fyne.Notification{
				Title:   "No Clients Available",
				Content: "Connect a client to switch control",
			})
			a.server.RUnlockClientsMutex()
			return
		}

		var targetClient *server.ClientConnection
		var targetAddr string
		for addr, client := range clients {
			if client.MonitorInfo != nil {
				targetClient = client
				targetAddr = addr
				break
			}
		}
		a.server.RUnlockClientsMutex()

		if targetClient == nil || targetClient.MonitorInfo == nil {
			fyne.CurrentApp().SendNotification(&fyne.Notification{
				Title:   "No Valid Clients",
				Content: "Client has no monitor information",
			})
			return
		}

		// Get monitor info for client's first screen
		clientScreen := targetClient.MonitorInfo.Screens[0]

		// Position cursor in center of client's first screen
		initialClientX := clientScreen.X + clientScreen.W/2
		initialClientY := clientScreen.Y + clientScreen.H/2

		// Update state
		a.server.SetRemoteInputActive(true)
		a.server.SetActiveClientAddr(targetAddr)
		a.server.SetLastSentMousePos(initialClientX, initialClientY)

		// Send mouse event to position cursor
		initMouseEvent := types.MouseEvent{
			X:      initialClientX,
			Y:      initialClientY,
			Action: types.ActionMove,
		}

		err := a.server.SendMessage(targetClient, types.TypeMouseEvent, initMouseEvent)
		if err != nil {
			log.Printf("Error sending initial mouse event: %v", err)
			a.server.SetRemoteInputActive(false)
			a.server.SetActiveClientAddr("")
			return
		}

		// Move server cursor off-screen
		robotgo.MoveMouse(-1, -1)

		// Notify user about switch
		fyne.CurrentApp().SendNotification(&fyne.Notification{
			Title:   "Control Switched",
			Content: "Now controlling client: " + targetAddr,
		})
	})

	switchToServerBtn := widget.NewButtonWithIcon("Switch to Server", theme.HomeIcon(), func() {
		// Check if we need to switch
		isRemote := a.server.IsRemoteInputActive()

		if !isRemote {
			fyne.CurrentApp().SendNotification(&fyne.Notification{
				Title:   "Already in Control",
				Content: "Already controlling server",
			})
			return
		}

		// Get server screens
		serverScreens := a.server.GetServerScreens()
		if len(serverScreens) == 0 {
			fyne.CurrentApp().SendNotification(&fyne.Notification{
				Title:   "Error",
				Content: "No server screens available",
			})
			return
		}

		// Position cursor in center of server's first screen
		serverScreen := serverScreens[0]
		entryX := serverScreen.X + serverScreen.W/2
		entryY := serverScreen.Y + serverScreen.H/2

		// Update state
		a.server.SetRemoteInputActive(false)
		a.server.SetActiveClientAddr("")

		// Move cursor to server screen
		robotgo.Move(entryX, entryY)

		// Notify user
		fyne.CurrentApp().SendNotification(&fyne.Notification{
			Title:   "Control Switched",
			Content: "Now controlling server",
		})
	})

	// Add a button to show shortcuts
	showShortcutsBtn := widget.NewButtonWithIcon("Keyboard Shortcuts", theme.HelpIcon(), func() {
		shortcutsWindow := a.fyneApp.NewWindow("Keyboard Shortcuts")
		shortcutsWindow.SetContent(container.NewVBox(
			widget.NewLabelWithStyle("Keyboard Shortcuts for Screen Control", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			widget.NewLabel("F1: Switch to client"),
			widget.NewLabel("F2: Switch to server"),
			widget.NewLabel("Option+Right Arrow: Switch to client"),
			widget.NewLabel("Option+Left Arrow: Switch to server"),
			widget.NewLabel("Command+Tab: Switch to client"),
			widget.NewLabel("Command+Backtick: Switch to server"),
		))
		shortcutsWindow.Resize(fyne.NewSize(300, 200))
		shortcutsWindow.Show()
	})

	controlContainer := container.NewHBox(
		switchToClientBtn,
		switchToServerBtn,
		showShortcutsBtn,
	)

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
		widget.NewLabel("Screen Control:"),
		controlContainer,
		widget.NewSeparator(),
		widget.NewLabel("Keyboard shortcuts are also available. Click the button above to see them."),
		widget.NewSeparator(),
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
				servers := []string{}                                   // For display list
				newServerMap := make(map[string]types.DiscoveryMessage) // For lookup

				// Build display list and map
				for _, info := range found {
					displayStr := fmt.Sprintf("%s (%s - %s)", info.Hostname, info.ServerIP, info.OS)
					servers = append(servers, displayStr)
					newServerMap[displayStr] = info
				}

				// Check for auto-connect condition
				if len(found) == 1 {
					// Double-check map has exactly one entry
					if len(found) == 1 {
						log.Println("Found exactly one server, attempting auto-connect...")
						var serverInfo types.DiscoveryMessage
						// Iterate map to get the single value (key doesn't matter here)
						for _, info := range found {
							serverInfo = info
							break // Found the one, exit loop
						}
						addr := fmt.Sprintf("%s:%d", serverInfo.ServerIP, serverInfo.Port) // Build address from the extracted value

						// Stop ticker and attempt connection
						close(stopTickerChan)
						a.attemptClientConnect(addr)
						return // Exit this goroutine
					} else {
						// This case should logically not be reached if len(found) == 1
						log.Println("Error: Inconsistent state during auto-connect check.")
					}
				}

				// Update UI list only if not auto-connecting
				discoveredServersBinding.Set(servers)
				serverMap = newServerMap // Update the map used by OnSelected

			case <-stopTickerChan:
				log.Println("Stopping discovery refresh ticker.")
				return
			}
		}
	}()

	connectBtn := widget.NewButton("Connect", func() {
		serverAddr := ipEntry.Text
		close(stopTickerChan)              // Stop discovery refresh
		a.attemptClientConnect(serverAddr) // Use the extracted connect logic
	})

	backBtn := widget.NewButton("Back", func() {
		close(stopTickerChan) // Stop discovery refresh
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

// attemptClientConnect tries to connect to the given server address and updates UI.
func (a *AppUI) attemptClientConnect(serverAddr string) {
	if serverAddr == "" {
		// TODO: Show a user message/dialog about empty address
		log.Println("Attempted connect with empty address.")
		return
	}
	if a.client == nil {
		log.Println("Connect attempt failed: client is nil.")
		// Maybe recreate client or go back to main menu?
		// For now, just return to avoid panic
		return
	}

	log.Printf("Attempting to connect to %s...", serverAddr)
	err := a.client.Connect(serverAddr)
	if err != nil {
		log.Printf("Error connecting: %v", err)
		// Display error on the client UI page
		errorLabel := widget.NewLabel(fmt.Sprintf("Error connecting to %s: %v", serverAddr, err))
		backToDiscoveryBtn := widget.NewButton("Back to Discovery", func() {
			a.startClientUI() // Go back to client discovery UI
		})
		a.mainWin.SetContent(container.NewVBox(errorLabel, backToDiscoveryBtn))
		return
	}
	// Connection successful
	log.Println("Connection successful.")
	a.showClientStatusScreen()
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

	// Initial Status Labels
	statusLabel := widget.NewLabel(fmt.Sprintf("Connected to: %s (%s)", serverInfo.Hostname, serverInfo.ServerIP))
	localLabel := widget.NewLabel(fmt.Sprintf("Local: %s (%s)", localHostname, localOs))
	remoteOsLabel := widget.NewLabel(fmt.Sprintf("Remote OS: %s", serverInfo.OS))

	// --- Button and Container Setup ---
	// Use a placeholder for the button initially
	buttonContainer := container.NewMax()
	infoContainer := container.NewVBox(
		statusLabel,
		remoteOsLabel,
		widget.NewSeparator(),
		localLabel,
		widget.NewLabel("Press ESC to disconnect (or use button)"),
		buttonContainer, // Placeholder for the button
	)
	a.mainWin.SetContent(infoContainer)

	// --- UI Polling Goroutine ---
	stopPoller := make(chan struct{})
	uiUpdateTicker := time.NewTicker(2 * time.Second)

	go func() {
		defer uiUpdateTicker.Stop()
		log.Println("Starting Client UI status poller...")
		for {
			select {
			case <-uiUpdateTicker.C:
				if a.client == nil {
					return
				} // Exit if client is gone

				connected := a.client.IsConnected()
				reconnecting := a.client.IsReconnecting()

				// Check current button type to avoid unnecessary updates
				currentButton := buttonContainer.Objects[0] // Assume button is always the first/only object

				if reconnecting {
					statusLabel.SetText(fmt.Sprintf("Connection lost. Reconnecting to %s...", a.client.LastServerAddr))
					// Change button to Cancel Reconnect if not already
					if _, ok := currentButton.(*widget.Button); !ok || currentButton.(*widget.Button).Text != "Cancel Reconnect" {
						cancelBtn := widget.NewButton("Cancel Reconnect", func() {
							if a.client != nil {
								a.client.StopReconnectIfNeeded()
							}
							close(stopPoller) // Stop this poller
							a.startClientUI() // Go back to discovery
						})
						buttonContainer.Objects = []fyne.CanvasObject{cancelBtn}
						buttonContainer.Refresh()
					}
				} else if connected {
					// Ensure status shows connected (might have reconnected)
					serverInfo := a.client.GetConnectedServerInfo() // Re-get info in case it changed
					statusLabel.SetText(fmt.Sprintf("Connected to: %s (%s)", serverInfo.Hostname, serverInfo.ServerIP))
					// Change button to Disconnect if not already
					if _, ok := currentButton.(*widget.Button); !ok || currentButton.(*widget.Button).Text != "Disconnect" {
						disconnectBtn := widget.NewButton("Disconnect", func() {
							close(stopPoller) // Stop this poller
							if a.client != nil {
								a.client.Stop() // Normal stop (also stops reconnect)
								a.client = nil
							}
							a.mainWin.SetContent(a.createMainContent())
						})
						buttonContainer.Objects = []fyne.CanvasObject{disconnectBtn}
						buttonContainer.Refresh()
					}
				} else { // Not connected and not reconnecting
					log.Println("Client disconnected and not reconnecting. Returning to discovery.")
					close(stopPoller)
					a.startClientUI()
					return // Exit goroutine
				}

			case <-stopPoller:
				log.Println("Stopping Client UI status poller.")
				return // Exit goroutine
			}
		}
	}()

	// Set initial button state (must be Disconnect initially)
	disconnectBtn := widget.NewButton("Disconnect", func() {
		close(stopPoller) // Stop the poller
		if a.client != nil {
			a.client.Stop() // Normal stop
			a.client = nil
		}
		a.mainWin.SetContent(a.createMainContent())
	})
	buttonContainer.Objects = []fyne.CanvasObject{disconnectBtn}
	buttonContainer.Refresh()
}

// findPrimaryMonitorIndex identifies the likely primary monitor (contains 0,0 or top-leftmost).
func findPrimaryMonitorIndex(screens []types.ScreenRect) int {
	if len(screens) == 0 {
		return -1 // No screens
	}
	topLeftIndex := 0
	minX, minY := screens[0].X, screens[0].Y
	containsOrigin := false
	for i, s := range screens {
		if s.X == 0 && s.Y == 0 {
			return i // Found the one at origin
		}
		if !containsOrigin {
			if s.X <= 0 && s.Y <= 0 && (s.X+s.W) > 0 && (s.Y+s.H) > 0 {
				containsOrigin = true
				topLeftIndex = i      // First one found containing origin
				minX, minY = s.X, s.Y // Update min values relative to this one
			} else if s.X < minX || (s.X == minX && s.Y < minY) {
				// If no screen contains origin yet, track top-leftmost
				minX = s.X
				minY = s.Y
				topLeftIndex = i
			}
		}
	}
	return topLeftIndex
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
	windowLayout := make(map[string]*types.VirtualScreen) // Tracks widget instances and their logical layout
	widgetMap := make(map[string]*monitorWidget)          // Tracks the actual widgets for positioning

	scale := float32(0.1)
	padding := float32(30) // Increased padding between server/client groups

	// Find primary monitors
	serverPrimaryIndex := findPrimaryMonitorIndex(serverInfo.Screens)
	clientPrimaryIndex := findPrimaryMonitorIndex(clientInfo.Screens)

	if serverPrimaryIndex == -1 && clientPrimaryIndex == -1 {
		log.Println("Warning: No screen info available for monitor management window for", clientAddr)
		monWin.SetContent(widget.NewLabel("No screen information received from server or client."))
		monWin.Resize(fyne.NewSize(300, 100))
		monWin.Show()
		return
	}

	// Store initial relative positions for layout calculation
	initialPositions := make(map[string]fyne.Position)

	// Calculate initial position for server primary
	var serverPrimaryWidgetPos fyne.Position
	if serverPrimaryIndex != -1 {
		serverPrimaryWidgetPos = fyne.NewPos(padding, padding) // Start server primary top-left
		serverPrimaryScreen := serverInfo.Screens[serverPrimaryIndex]
		key := types.ScreenKey(serverInfo.Hostname, serverPrimaryScreen.ID)
		initialPositions[key] = serverPrimaryWidgetPos
	}

	// Calculate initial position for client primary relative to server primary
	var clientPrimaryWidgetPos fyne.Position
	if clientPrimaryIndex != -1 {
		clientPrimaryScreen := clientInfo.Screens[clientPrimaryIndex]
		if serverPrimaryIndex != -1 {
			// Place client primary to the right of server primary
			serverPrimaryScreen := serverInfo.Screens[serverPrimaryIndex]
			serverPrimaryWidgetWidth := float32(serverPrimaryScreen.W) * scale
			clientPrimaryWidgetPos = fyne.NewPos(serverPrimaryWidgetPos.X+serverPrimaryWidgetWidth+padding, serverPrimaryWidgetPos.Y)
		} else {
			// If no server primary, place client primary top-left
			clientPrimaryWidgetPos = fyne.NewPos(padding, padding)
		}
		key := types.ScreenKey(clientInfo.Hostname, clientPrimaryScreen.ID)
		initialPositions[key] = clientPrimaryWidgetPos
	}

	// Function to add and position monitors relatively
	addAndPositionMonitors := func(info types.MonitorInfo, primaryIndex int, isServer bool) {
		if primaryIndex == -1 || len(info.Screens) == 0 {
			return // Skip if no primary or no screens
		}
		primaryScreen := info.Screens[primaryIndex]
		primaryKey := types.ScreenKey(info.Hostname, primaryScreen.ID)
		primaryWidgetPos := initialPositions[primaryKey]

		for i, screen := range info.Screens {
			mw := NewMonitorWidget(screen, info.Hostname, isServer)
			layoutCanvas.Add(mw)
			key := types.ScreenKey(info.Hostname, screen.ID)
			widgetMap[key] = mw // Store widget reference

			scaledW := float32(screen.W) * scale
			scaledH := float32(screen.H) * scale
			mw.Resize(fyne.NewSize(scaledW, scaledH))

			var currentWidgetPos fyne.Position
			if i == primaryIndex {
				currentWidgetPos = primaryWidgetPos
			} else {
				// Calculate position relative to the primary monitor's widget
				deltaX := float32(screen.X-primaryScreen.X) * scale
				deltaY := float32(screen.Y-primaryScreen.Y) * scale
				currentWidgetPos = fyne.NewPos(primaryWidgetPos.X+deltaX, primaryWidgetPos.Y+deltaY)
			}
			mw.Move(currentWidgetPos)

			// Create VirtualScreen entry for layout state
			vScreen := &types.VirtualScreen{
				ID:       screen.ID,
				Hostname: info.Hostname,
				IsServer: isServer,
				Original: screen,
				Position: types.Position{X: currentWidgetPos.X, Y: currentWidgetPos.Y}, // Store initial widget pos
				Size:     types.Size{Width: scaledW, Height: scaledH},
			}
			windowLayout[key] = vScreen

			// DragEnd callback with snapping
			mw.OnDragEnd = func(draggedWidget *monitorWidget) {
				const snapThreshold = float32(15.0) // Max distance to snap
				const minOverlapRatio = 0.3         // Minimum overlap needed to snap edges

				finalPos := draggedWidget.Position()
				draggedSize := draggedWidget.Size()
				draggedKey := types.ScreenKey(draggedWidget.hostname, draggedWidget.screenInfo.ID)

				bestSnapDeltaX := float32(math.Inf(1))
				bestSnapDeltaY := float32(math.Inf(1))
				snappedX := false
				snappedY := false

				// Iterate through all *other* widgets to find snap candidates
				for otherKey, otherWidget := range widgetMap {
					if otherKey == draggedKey {
						continue // Don't snap to self
					}
					otherPos := otherWidget.Position()
					otherSize := otherWidget.Size()

					// Calculate vertical overlap range and length
					overlapYStart := max(finalPos.Y, otherPos.Y)
					overlapYEnd := min(finalPos.Y+draggedSize.Height, otherPos.Y+otherSize.Height)
					overlapYLen := overlapYEnd - overlapYStart

					// Calculate horizontal overlap range and length
					overlapXStart := max(finalPos.X, otherPos.X)
					overlapXEnd := min(finalPos.X+draggedSize.Width, otherPos.X+otherSize.Width)
					overlapXLen := overlapXEnd - overlapXStart

					// --- Check Horizontal Snaps (Left/Right) ---
					if overlapYLen > minOverlapRatio*min(draggedSize.Height, otherSize.Height) { // Check for sufficient vertical overlap
						// Dragged Right edge to Other Left edge
						dx := otherPos.X - (finalPos.X + draggedSize.Width)
						if abs(dx) < snapThreshold && abs(dx) < abs(bestSnapDeltaX) {
							bestSnapDeltaX = dx
							snappedX = true
						}
						// Dragged Left edge to Other Right edge
						dx = (otherPos.X + otherSize.Width) - finalPos.X
						if abs(dx) < snapThreshold && abs(dx) < abs(bestSnapDeltaX) {
							bestSnapDeltaX = dx
							snappedX = true
						}
						// Dragged Left edge to Other Left edge
						dx = otherPos.X - finalPos.X
						if abs(dx) < snapThreshold && abs(dx) < abs(bestSnapDeltaX) {
							bestSnapDeltaX = dx
							snappedX = true
						}
						// Dragged Right edge to Other Right edge
						dx = (otherPos.X + otherSize.Width) - (finalPos.X + draggedSize.Width)
						if abs(dx) < snapThreshold && abs(dx) < abs(bestSnapDeltaX) {
							bestSnapDeltaX = dx
							snappedX = true
						}
					}

					// --- Check Vertical Snaps (Top/Bottom) ---
					if overlapXLen > minOverlapRatio*min(draggedSize.Width, otherSize.Width) { // Check for sufficient horizontal overlap
						// Dragged Bottom edge to Other Top edge
						dy := otherPos.Y - (finalPos.Y + draggedSize.Height)
						if abs(dy) < snapThreshold && abs(dy) < abs(bestSnapDeltaY) {
							bestSnapDeltaY = dy
							snappedY = true
						}
						// Dragged Top edge to Other Bottom edge
						dy = (otherPos.Y + otherSize.Height) - finalPos.Y
						if abs(dy) < snapThreshold && abs(dy) < abs(bestSnapDeltaY) {
							bestSnapDeltaY = dy
							snappedY = true
						}
						// Dragged Top edge to Other Top edge
						dy = otherPos.Y - finalPos.Y
						if abs(dy) < snapThreshold && abs(dy) < abs(bestSnapDeltaY) {
							bestSnapDeltaY = dy
							snappedY = true
						}
						// Dragged Bottom edge to Other Bottom edge
						dy = (otherPos.Y + otherSize.Height) - (finalPos.Y + draggedSize.Height)
						if abs(dy) < snapThreshold && abs(dy) < abs(bestSnapDeltaY) {
							bestSnapDeltaY = dy
							snappedY = true
						}
					}
				}

				// Apply the best snap found (preferring the closer snap if both X and Y are possible)
				finalSnappedPos := finalPos
				if snappedX && (!snappedY || abs(bestSnapDeltaX) <= abs(bestSnapDeltaY)) {
					finalSnappedPos.X += bestSnapDeltaX
					log.Printf("Snapped X by %.1f", bestSnapDeltaX)
				} else if snappedY {
					finalSnappedPos.Y += bestSnapDeltaY
					log.Printf("Snapped Y by %.1f", bestSnapDeltaY)
				}

				// Move the widget visually to the snapped position
				draggedWidget.Move(finalSnappedPos)

				// Update the layout state with the final snapped position
				if vScreen, ok := windowLayout[draggedKey]; ok {
					vScreen.Position = types.Position{X: finalSnappedPos.X, Y: finalSnappedPos.Y}
				}

				// Recalculate layout configuration based on the new (snapped) positions
				layoutConfig := calculateLayoutConfiguration(windowLayout)
				if a.server != nil {
					a.server.UpdateLayout(clientAddr, layoutConfig)
				} else {
					log.Println("Error: Server is nil, cannot update layout.")
				}
			}
		}
	}

	// Add monitors using the relative positioning logic
	addAndPositionMonitors(serverInfo, serverPrimaryIndex, true)
	addAndPositionMonitors(clientInfo, clientPrimaryIndex, false)

	// Calculate required canvas size based on placed widgets
	minWidgetX, minWidgetY := float32(math.Inf(1)), float32(math.Inf(1))
	maxWidgetX, maxWidgetY := float32(math.Inf(-1)), float32(math.Inf(-1))

	if len(widgetMap) == 0 { // Handle case with no widgets added
		minWidgetX, minWidgetY = 0, 0
		maxWidgetX, maxWidgetY = 2*padding, 2*padding // Default small size
	} else {
		for _, widget := range widgetMap {
			pos := widget.Position()
			size := widget.Size()
			minWidgetX = min(minWidgetX, pos.X)
			minWidgetY = min(minWidgetY, pos.Y)
			maxWidgetX = max(maxWidgetX, pos.X+size.Width)
			maxWidgetY = max(maxWidgetY, pos.Y+size.Height)
		}
	}

	// Ensure minimum padding around the content
	totalWidth := maxWidgetX + padding // Add padding to the right/bottom edge
	totalHeight := maxWidgetY + padding
	// If minWidgetX/Y is less than padding, we need to shift everything and increase size
	shiftX := float32(0)
	shiftY := float32(0)
	if minWidgetX < padding {
		shiftX = padding - minWidgetX
		totalWidth += shiftX
	}
	if minWidgetY < padding {
		shiftY = padding - minWidgetY
		totalHeight += shiftY
	}

	// Apply shift if necessary
	if shiftX > 0 || shiftY > 0 {
		for _, widget := range widgetMap {
			widget.Move(widget.Position().Add(fyne.NewPos(shiftX, shiftY)))
		}
		// Update positions in windowLayout as well
		for _, vScreen := range windowLayout {
			vScreen.Position.X += shiftX
			vScreen.Position.Y += shiftY
		}
	}

	// Update the server's master layout with the calculated initial state
	if a.server != nil {
		initialLayoutConfig := calculateLayoutConfiguration(windowLayout)
		a.server.UpdateLayout(clientAddr, initialLayoutConfig)
	}

	content := layoutCanvas
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

// Helper functions for layout calculations
func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func min(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
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
