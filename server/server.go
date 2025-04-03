package server

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"kb/types"

	"github.com/go-vgo/robotgo"
	hook "github.com/robotn/gohook"
	"golang.org/x/exp/constraints"
)

// Removed local type definitions - now in kb/types

const (
	DiscoveryAddr     = "239.0.0.1:9999"
	DiscoveryType     = "KB_SHARE_DISCOVERY_V1" // Use string literal here
	BroadcastInterval = 5 * time.Second
)

type ClientConnection struct {
	Conn        net.Conn
	Encoder     *json.Encoder
	Decoder     *json.Decoder
	MonitorInfo *types.MonitorInfo // Store client monitor info when received
}

type Server struct {
	listener net.Listener
	// Use a map for easier client management [RemoteAddr] -> ClientConnection
	clients       map[string]*ClientConnection
	clientsMutex  sync.RWMutex // Mutex to protect the clients map
	stopChan      chan struct{}
	discoveryStop chan struct{}
	// Channel to signal UI about new client connections / monitor info updates
	ClientUpdateChan chan *ClientConnection
	// Channel to signal UI about startup warnings (e.g., permissions)
	WarningChan chan string

	// Input Redirection State
	remoteInputActive bool   // Is input currently directed to a client?
	activeClientAddr  string // Which client address is receiving input?
	lastSentMouseX    int    // Store last sent/calculated coords for return check
	lastSentMouseY    int

	// Layout Management
	layoutConfigs      map[string]*types.LayoutConfiguration // map[clientAddr]*LayoutConfiguration
	layoutConfigsMutex sync.RWMutex
	serverScreens      []types.ScreenRect
}

// getLocalMonitorInfo gathers information about the server's monitors
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

func NewServer(port int) (*Server, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}

	s := &Server{
		listener:          listener,
		clients:           make(map[string]*ClientConnection),
		stopChan:          make(chan struct{}),
		discoveryStop:     make(chan struct{}),
		ClientUpdateChan:  make(chan *ClientConnection, 5),
		WarningChan:       make(chan string, 1), // Buffer 1 for startup warning
		remoteInputActive: false,
		activeClientAddr:  "",
		layoutConfigs:     make(map[string]*types.LayoutConfiguration),
		serverScreens:     getLocalMonitorInfo().Screens,
	}
	return s, nil
}

// Send a wrapped message to a specific client
func (s *Server) sendMessage(client *ClientConnection, msgType types.MessageType, payload interface{}) error {
	wrappedMsg := types.WrappedMessage{
		Type:    msgType,
		Payload: payload,
	}
	// Use the client's dedicated encoder
	return client.Encoder.Encode(wrappedMsg)
}

// Start begins the server's operations: accepting connections and capturing input.
func (s *Server) Start() {
	go s.acceptConnections()
	go s.startDiscoveryBroadcaster()
	go s.captureAndTrackInput() // Combined input handler
}

func (s *Server) Stop() {
	close(s.stopChan)
	close(s.discoveryStop)
	s.listener.Close()
	s.clientsMutex.Lock()
	for _, client := range s.clients {
		client.Conn.Close()
	}
	s.clients = make(map[string]*ClientConnection) // Clear map
	s.clientsMutex.Unlock()
	close(s.ClientUpdateChan) // Close update channel
}

func (s *Server) acceptConnections() {
	serverMonitorInfo := getLocalMonitorInfo()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopChan:
				return // Server is stopping
			default:
				fmt.Printf("Error accepting connection: %v\n", err)
				continue
			}
		}

		clientAddr := conn.RemoteAddr().String()
		clientConn := &ClientConnection{
			Conn:    conn,
			Encoder: json.NewEncoder(conn),
			Decoder: json.NewDecoder(conn),
		}

		s.clientsMutex.Lock()
		s.clients[clientAddr] = clientConn
		s.clientsMutex.Unlock()

		fmt.Printf("New client connected: %s\n", clientAddr)

		// Send server monitor info immediately
		err = s.sendMessage(clientConn, types.TypeMonitorInfo, serverMonitorInfo)
		if err != nil {
			fmt.Printf("Error sending server monitor info to %s: %v\n", clientAddr, err)
			s.removeClient(clientAddr)
			continue
		}

		// Start a goroutine to handle messages from this client
		go s.handleClientMessages(clientConn)
	}
}

// handleClientMessages runs in a goroutine for each connected client
func (s *Server) handleClientMessages(client *ClientConnection) {
	clientAddr := client.Conn.RemoteAddr().String()
	defer s.removeClient(clientAddr)

	for {
		var wrappedMsg types.WrappedMessage
		err := client.Decoder.Decode(&wrappedMsg)
		if err != nil {
			select {
			case <-s.stopChan: // Check if server is stopping
				return
			default:
				if err != nil {
					fmt.Printf("Error decoding message from %s: %v\n", clientAddr, err)
					return // Close connection on decode error
				}
			}
		}

		switch wrappedMsg.Type {
		case types.TypeMonitorInfo:
			// Need to decode the payload map into the correct struct
			payloadBytes, _ := json.Marshal(wrappedMsg.Payload)
			var monitorInfo types.MonitorInfo
			err := json.Unmarshal(payloadBytes, &monitorInfo)
			if err != nil {
				fmt.Printf("Error unmarshaling MonitorInfo from %s: %v\n", clientAddr, err)
				continue
			}
			fmt.Printf("Received MonitorInfo from %s: %+v\n", clientAddr, monitorInfo)
			s.clientsMutex.Lock()
			client.MonitorInfo = &monitorInfo // Store it
			s.clientsMutex.Unlock()
			// Signal UI about the update
			s.ClientUpdateChan <- client

		// Handle other message types later (e.g., acknowledgements, mouse data)
		default:
			fmt.Printf("Received unhandled message type '%s' from %s\n", wrappedMsg.Type, clientAddr)
		}
	}
}

// removeClient closes connection and removes client from the map
func (s *Server) removeClient(addr string) {
	s.clientsMutex.Lock()
	client, ok := s.clients[addr]
	if ok {
		client.Conn.Close()
		delete(s.clients, addr)
		log.Printf("Client disconnected: %s\n", addr)
	}
	s.clientsMutex.Unlock()

	// Remove layout config for the disconnected client
	s.layoutConfigsMutex.Lock()
	delete(s.layoutConfigs, addr)
	s.layoutConfigsMutex.Unlock()

	// If this was the active client, reset remote input state
	s.clientsMutex.Lock() // Use main mutex for state vars
	if s.activeClientAddr == addr {
		s.remoteInputActive = false
		s.activeClientAddr = ""
		log.Println("Active client disconnected, reverting to local input.")
	}
	s.clientsMutex.Unlock()
}

// captureAndTrackInput captures keyboard and mouse events and handles redirection.
func (s *Server) captureAndTrackInput() {
	log.Println("Starting unified input capture...")
	evChan := hook.Start()
	// Check for Accessibility permission error specifically on macOS
	if runtime.GOOS == "darwin" {
		permissionOk := true // Assume OK initially
		if evChan == nil {
			log.Println("Warning: hook.Start() returned nil channel, likely missing macOS Accessibility permissions.")
			permissionOk = false
		} else {
			// Check if channel is immediately closed (non-blocking)
			select {
			case _, ok := <-evChan:
				if !ok { // Channel is closed
					log.Println("Warning: Event channel closed unexpectedly, likely missing macOS Accessibility permissions.")
					permissionOk = false
				} else {
					// Received an event immediately? This is odd, but maybe not a permission error.
					log.Println("Warning: Unexpected event received immediately after hook start.")
					// We could potentially try putting the event back on a buffered channel
					// but let's assume it's not fatal for now.
				}
			default:
				// Channel is open and no immediate event, likely OK.
			}
		}

		if !permissionOk {
			// Send warning to UI only if a problem was detected
			select {
			case s.WarningChan <- "macOS Accessibility permission likely missing.":
			default:
			}
		}
		// Proceed regardless of the warning, allow hook registration attempt.
	}
	defer hook.End()

	log.Println("Input capture hook started successfully.")

	// --- Register Keyboard Hooks (Signature: func(e hook.Event)) ---
	hook.Register(hook.KeyDown, []string{}, func(e hook.Event) {
		// Debug logging for all key events
		log.Printf("DEBUG: KeyDown event - Keychar: %s, Rawcode: %d, Keycode: %d, Mask: %d",
			string(e.Keychar), e.Rawcode, e.Keycode, e.Mask)

		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		s.clientsMutex.RUnlock()

		// Handle keyboard shortcuts for explicit monitor switching
		// On Mac, right arrow is 124 and left arrow is 123
		// Try multiple keyboard combinations
		if (e.Rawcode == 124 && ((e.Mask&4 != 0) || (e.Mask&8 != 0))) || // Right Arrow + (Alt or Cmd)
			(e.Rawcode == 48 && (e.Mask&8 != 0)) || // Tab key (48) + Cmd (8)
			e.Rawcode == 122 { // F1 key (122) - no modifier needed
			log.Printf("SHORTCUT: Detected shortcut (code %d) - forcing transition to client", e.Rawcode)

			// If we're already remote, this does nothing
			if isRemote {
				log.Printf("SHORTCUT: Already controlling client, ignoring shortcut")
				return
			}

			// Check for any available client to switch to
			s.clientsMutex.RLock()
			if len(s.clients) == 0 {
				log.Printf("SHORTCUT: No clients available to switch to")
				s.clientsMutex.RUnlock()
				return
			}

			// Pick first client in map (or specific one in future)
			var targetClient *ClientConnection
			var targetAddr string
			for addr, client := range s.clients {
				if client.MonitorInfo != nil {
					targetClient = client
					targetAddr = addr
					break
				}
			}

			if targetClient == nil || targetClient.MonitorInfo == nil {
				log.Printf("SHORTCUT: No clients with monitor info available")
				s.clientsMutex.RUnlock()
				return
			}

			// Get monitor info for client's first screen
			clientScreen := targetClient.MonitorInfo.Screens[0]
			s.clientsMutex.RUnlock()

			// Position cursor in center of client's first screen
			initialClientX := clientScreen.X + clientScreen.W/2
			initialClientY := clientScreen.Y + clientScreen.H/2

			log.Printf("SHORTCUT: Switching input to client: %s at position (%d,%d)",
				targetAddr, initialClientX, initialClientY)

			// Update state
			s.clientsMutex.Lock()
			s.remoteInputActive = true
			s.activeClientAddr = targetAddr
			s.lastSentMouseX = initialClientX
			s.lastSentMouseY = initialClientY
			activeClient := targetClient
			s.clientsMutex.Unlock()

			// Send mouse event to position cursor
			initMouseEvent := types.MouseEvent{
				X:      initialClientX,
				Y:      initialClientY,
				Action: types.ActionMove,
			}

			err := s.sendMessage(activeClient, types.TypeMouseEvent, initMouseEvent)
			if err != nil {
				log.Printf("SHORTCUT: Error sending initial mouse event: %v", err)
				s.clientsMutex.Lock()
				s.remoteInputActive = false
				s.activeClientAddr = ""
				s.clientsMutex.Unlock()
				return
			}

			// Move server cursor off-screen
			robotgo.MoveMouse(-1, -1)

			// Send a second event to ensure it's received
			time.Sleep(10 * time.Millisecond)
			_ = s.sendMessage(activeClient, types.TypeMouseEvent, initMouseEvent)

			return
		} else if (e.Rawcode == 123 && ((e.Mask&4 != 0) || (e.Mask&8 != 0))) || // Left Arrow + (Alt or Cmd)
			(e.Rawcode == 50 && (e.Mask&8 != 0)) || // Backtick key (50) + Cmd (8)
			e.Rawcode == 120 { // F2 key (120) - no modifier needed
			log.Printf("SHORTCUT: Detected shortcut (code %d) - forcing transition to server", e.Rawcode)

			// If we're already on server, this does nothing
			if !isRemote {
				log.Printf("SHORTCUT: Already controlling server, ignoring shortcut")
				return
			}

			// Get client info
			s.clientsMutex.RLock()
			client, clientOk := s.clients[activeAddr]
			s.clientsMutex.RUnlock()

			if !clientOk || client.MonitorInfo == nil {
				log.Printf("SHORTCUT: Active client no longer available")
				s.clientsMutex.Lock()
				s.remoteInputActive = false
				s.activeClientAddr = ""
				s.clientsMutex.Unlock()
				return
			}

			// Get first server screen
			serverScreens := s.GetServerScreens()
			if len(serverScreens) == 0 {
				log.Printf("SHORTCUT: No server screens available")
				return
			}
			serverScreen := serverScreens[0]

			// Position cursor in center of server's first screen
			entryX := serverScreen.X + serverScreen.W/2
			entryY := serverScreen.Y + serverScreen.H/2

			log.Printf("SHORTCUT: Switching input back to server at position (%d,%d)",
				entryX, entryY)

			// Update state
			s.clientsMutex.Lock()
			s.remoteInputActive = false
			s.activeClientAddr = ""
			s.clientsMutex.Unlock()

			// Move cursor to server screen
			robotgo.Move(entryX, entryY)

			return
		}

		if isRemote {
			s.clientsMutex.RLock()
			client, ok := s.clients[activeAddr]
			s.clientsMutex.RUnlock()
			if ok {
				keyEvent := types.KeyEvent{Type: "keydown", Keychar: string(e.Keychar)}
				err := s.sendMessage(client, types.TypeKeyEvent, keyEvent)
				if err != nil {
					log.Printf("Error sending keydown event to client %s: %v. Removing client.\n", activeAddr, err)
					go s.removeClient(activeAddr)
				}
			} else {
				log.Printf("Keydown: Active client %s not found.", activeAddr)
			}
			// Cannot block event propagation here.
		}
	})
	hook.Register(hook.KeyUp, []string{}, func(e hook.Event) {
		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		s.clientsMutex.RUnlock()

		if isRemote {
			s.clientsMutex.RLock()
			client, ok := s.clients[activeAddr]
			s.clientsMutex.RUnlock()
			if ok {
				keyEvent := types.KeyEvent{Type: "keyup", Keychar: string(e.Keychar)}
				err := s.sendMessage(client, types.TypeKeyEvent, keyEvent)
				if err != nil {
					log.Printf("Error sending keyup event to client %s: %v. Removing client.\n", activeAddr, err)
					go s.removeClient(activeAddr)
				}
			} else {
				log.Printf("Keyup: Active client %s not found.", activeAddr)
			}
			// Cannot block event propagation here.
		}
	})

	// --- Register Mouse Hook (Signature: func(e hook.Event)) ---
	hook.Register(hook.MouseMove, []string{}, func(e hook.Event) {
		log.Printf("MouseMove Event: (%d, %d)", int(e.X), int(e.Y)) // Enable debug logging
		x := int(e.X)
		y := int(e.Y)

		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		s.clientsMutex.RUnlock()

		if isRemote {
			// --- Currently controlling remote client ---
			switchToServer, targetServerScreen, targetServerEdge := s.checkReturnTransition(x, y, activeAddr) // Check using current event coords

			if switchToServer {
				log.Printf("TRANSITION: Switching input back to server from %s\n", activeAddr)
				s.clientsMutex.RLock()
				client, clientOk := s.clients[activeAddr]
				clientInfo := client.MonitorInfo // Can be nil if client disconnected quickly
				lastX := s.lastSentMouseX        // Use *last sent* coords to determine exit edge
				lastY := s.lastSentMouseY
				s.clientsMutex.RUnlock()

				var entryX, entryY int
				calculatedEntry := false

				if clientOk && clientInfo != nil {
					// Try to calculate entry point properly
					var sourceClientScreen types.ScreenRect
					var sourceClientEdge types.ScreenEdge = "" // Initialize as undetermined
					foundSourceScreen := false
					const returnEdgeBuffer = 2
					for _, cs := range clientInfo.Screens {
						if lastX >= cs.X && lastX < cs.X+cs.W && lastY >= cs.Y && lastY < cs.Y+cs.H {
							sourceClientScreen = cs
							foundSourceScreen = true
							// Determine edge based on last coords relative to this screen
							if lastX <= cs.X+returnEdgeBuffer {
								sourceClientEdge = types.EdgeLeft
							} else if lastX >= cs.X+cs.W-returnEdgeBuffer {
								sourceClientEdge = types.EdgeRight
							} else if lastY <= cs.Y+returnEdgeBuffer {
								sourceClientEdge = types.EdgeTop
							} else if lastY >= cs.Y+cs.H-returnEdgeBuffer {
								sourceClientEdge = types.EdgeBottom
							}
							break // Break after determining edge or finding the screen
						}
					}

					if foundSourceScreen && sourceClientEdge != "" {
						entryX, entryY = calculateEntryPoint(lastX, lastY, sourceClientScreen, sourceClientEdge, targetServerScreen, targetServerEdge)
						calculatedEntry = true
					} else {
						log.Printf("Warning: Could not determine client exit screen/edge from last coords (%d, %d). Using rough estimate.", lastX, lastY)
					}
				} else {
					log.Println("Cannot calculate return point: Client disconnected or missing info.")
				}

				if !calculatedEntry {
					// Fallback: Use the target edge to guess entry point or default to center
					entryX, entryY = targetServerScreen.X+targetServerScreen.W/2, targetServerScreen.Y+targetServerScreen.H/2
					if targetServerEdge == types.EdgeLeft {
						entryX = targetServerScreen.X + 3
					} else if targetServerEdge == types.EdgeRight {
						entryX = targetServerScreen.X + targetServerScreen.W - 3
					} else if targetServerEdge == types.EdgeTop {
						entryY = targetServerScreen.Y + 3
					} else if targetServerEdge == types.EdgeBottom {
						entryY = targetServerScreen.Y + targetServerScreen.H - 3
					}
				}

				// Update state *after* all calculations
				s.clientsMutex.Lock()
				s.remoteInputActive = false
				s.activeClientAddr = ""
				s.clientsMutex.Unlock()

				// Move cursor *after* releasing lock
				log.Printf("TRANSITION: Moving server cursor to (%d, %d)", entryX, entryY)
				robotgo.Move(entryX, entryY)

			} else { // Still controlling remote client
				s.clientsMutex.RLock()
				client, ok := s.clients[activeAddr]
				s.clientsMutex.RUnlock()
				if !ok {
					log.Printf("MouseMove: Active client %s not found. Reverting to local.", activeAddr)
					s.clientsMutex.Lock()
					s.remoteInputActive = false
					s.activeClientAddr = ""
					s.clientsMutex.Unlock()
					return // Exit callback
				}

				// Send mouse event
				mouseEvent := types.MouseEvent{
					X:      x,
					Y:      y,
					Action: types.ActionMove,
				}
				s.clientsMutex.Lock()
				s.lastSentMouseX = x
				s.lastSentMouseY = y
				s.clientsMutex.Unlock() // Release lock before sending potentially blocking network call

				log.Printf("Sending mouse event to client %s: X: %d, Y: %d", activeAddr, x, y)
				err := s.sendMessage(client, types.TypeMouseEvent, mouseEvent)
				if err != nil {
					log.Printf("Error sending mouse event to client %s: %v. Removing client.\n", activeAddr, err)
					go s.removeClient(activeAddr)
				}
				// Keep server cursor off-screen
				robotgo.MoveMouse(-1, -1)
			}

		} else {
			// --- Currently controlling local server ---
			log.Printf("MouseMove: Local control, checking edge transition for (%d, %d)", x, y) // Enable this log
			switchToClient, targetClientAddr, targetLink := s.checkEdgeTransition(x, y)

			if switchToClient {
				log.Printf("TRANSITION: Switching input to client: %s\n", targetClientAddr)
				// --- (Find source/target screens and calculate entry point) ---
				var sourceScreen types.ScreenRect
				for _, ss := range s.GetServerScreens() {
					if ss.ID == targetLink.FromScreenID {
						sourceScreen = ss
						break
					}
				}
				s.clientsMutex.RLock()
				targetClientConn, targetClientOk := s.clients[targetClientAddr]
				s.clientsMutex.RUnlock()
				if !targetClientOk || targetClientConn.MonitorInfo == nil {
					log.Printf("Cannot switch: Target client %s disconnected or has no monitor info.\n", targetClientAddr)
					return // Exit callback
				}
				var targetClientScreen types.ScreenRect
				for _, cs := range targetClientConn.MonitorInfo.Screens {
					if cs.ID == targetLink.ToScreenID {
						targetClientScreen = cs
						break
					}
				}
				if targetClientScreen.W == 0 {
					log.Printf("Cannot switch: Target client screen ID %d not found.\n", targetLink.ToScreenID)
					return // Exit callback
				}
				initialClientX, initialClientY := calculateEntryPoint(x, y, sourceScreen, targetLink.FromEdge, targetClientScreen, targetLink.ToEdge)

				// Update state and send initial message
				s.clientsMutex.Lock()
				s.remoteInputActive = true
				s.activeClientAddr = targetClientAddr
				s.lastSentMouseX = initialClientX
				s.lastSentMouseY = initialClientY
				activeClient, stillExists := s.clients[targetClientAddr]
				if !stillExists {
					log.Printf("Target client %s disconnected before input switch could complete.", targetClientAddr)
					s.remoteInputActive = false
					s.activeClientAddr = ""
					s.clientsMutex.Unlock()
					return // Exit callback
				}
				// Send initial message *before* unlocking mutex
				log.Printf("TRANSITION: Moving client cursor to initial position (%d, %d)", initialClientX, initialClientY)
				initMouseEvent := types.MouseEvent{
					X:      initialClientX,
					Y:      initialClientY,
					Action: types.ActionMove,
				}
				err := s.sendMessage(activeClient, types.TypeMouseEvent, initMouseEvent)
				if err != nil {
					log.Printf("Error sending initial mouse event to client %s: %v. Reverting switch.\n", targetClientAddr, err)
					s.remoteInputActive = false
					s.activeClientAddr = ""
					// Unlock before removing client
					s.clientsMutex.Unlock()
					go s.removeClient(targetClientAddr)
					return // Exit callback
				}
				s.clientsMutex.Unlock() // Unlock after successful initial send

				// Move server cursor off-screen after switching
				log.Printf("TRANSITION: Moving server cursor off-screen")
				robotgo.MoveMouse(-1, -1)

				// Send a second mouse move event after a short delay to ensure the client receives it
				time.Sleep(10 * time.Millisecond)
				if s.clients[targetClientAddr] != nil {
					followupEvent := types.MouseEvent{
						X:      initialClientX,
						Y:      initialClientY,
						Action: types.ActionMove,
					}
					log.Printf("TRANSITION: Sending followup mouse event to ensure receipt")
					_ = s.sendMessage(s.clients[targetClientAddr], types.TypeMouseEvent, followupEvent)
				}
			}
			// If local and no switch happened, do nothing (allow OS handle)
		}
	})

	// Register mouse button events
	hook.Register(hook.MouseDown, []string{}, func(e hook.Event) {
		log.Printf("MouseDown Event: button %d at (%d, %d)", e.Button, int(e.X), int(e.Y))

		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		s.clientsMutex.RUnlock()

		if isRemote {
			s.clientsMutex.RLock()
			client, ok := s.clients[activeAddr]
			s.clientsMutex.RUnlock()
			if !ok {
				return
			}

			// Map button number to type
			button := types.ButtonLeft
			if e.Button == 2 {
				button = types.ButtonRight
			} else if e.Button == 3 {
				button = types.ButtonMiddle
			}

			// Send mouse down event
			mouseEvent := types.MouseEvent{
				X:      int(e.X),
				Y:      int(e.Y),
				Button: button,
				Action: types.ActionDown,
			}

			log.Printf("Sending mouse down to client %s: Button: %s at (%d, %d)",
				activeAddr, button, int(e.X), int(e.Y))
			err := s.sendMessage(client, types.TypeMouseEvent, mouseEvent)
			if err != nil {
				log.Printf("Error sending mouse down to client %s: %v. Removing client.",
					activeAddr, err)
				go s.removeClient(activeAddr)
			}
		}
	})

	hook.Register(hook.MouseUp, []string{}, func(e hook.Event) {
		log.Printf("MouseUp Event: button %d at (%d, %d)", e.Button, int(e.X), int(e.Y))

		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		s.clientsMutex.RUnlock()

		if isRemote {
			s.clientsMutex.RLock()
			client, ok := s.clients[activeAddr]
			s.clientsMutex.RUnlock()
			if !ok {
				return
			}

			// Map button number to type
			button := types.ButtonLeft
			if e.Button == 2 {
				button = types.ButtonRight
			} else if e.Button == 3 {
				button = types.ButtonMiddle
			}

			// Send mouse up event
			mouseEvent := types.MouseEvent{
				X:      int(e.X),
				Y:      int(e.Y),
				Button: button,
				Action: types.ActionUp,
			}

			log.Printf("Sending mouse up to client %s: Button: %s at (%d, %d)",
				activeAddr, button, int(e.X), int(e.Y))
			err := s.sendMessage(client, types.TypeMouseEvent, mouseEvent)
			if err != nil {
				log.Printf("Error sending mouse up to client %s: %v. Removing client.",
					activeAddr, err)
				go s.removeClient(activeAddr)
			}
		}
	})

	// --- Main Event Loop ---
	for {
		select {
		case <-s.stopChan:
			log.Println("Stopping input capture loop.")
			return
		case <-evChan:
			// Events are handled by registered callbacks
			// log.Printf("Received event on evChan (type %T)", e) // Uncomment for basic channel check
		}
	}
}

// broadcastEvent - This is no longer suitable for key events as they go to a specific client.
// We might need a different broadcast mechanism for other message types later.
/* func (s *Server) broadcastEvent(event types.KeyEvent) { ... } */

// startDiscoveryBroadcaster periodically sends out UDP multicast messages
func (s *Server) startDiscoveryBroadcaster() {
	addr, err := net.ResolveUDPAddr("udp", DiscoveryAddr)
	if err != nil {
		fmt.Printf("Error resolving UDP address for discovery: %v\n", err)
		return
	}

	conn, err := net.DialUDP("udp", nil, addr) // Use DialUDP for sending
	if err != nil {
		fmt.Printf("Error dialing UDP for discovery: %v\n", err)
		return
	}
	defer conn.Close()

	hostname, _ := os.Hostname()
	localIP, err := getLocalIP()
	if err != nil {
		fmt.Printf("Error getting local IP for discovery: %v\n", err)
		// Fallback or handle error appropriately
		localIP = "UNKNOWN"
	}
	serverPort := s.GetListenPort()
	if serverPort == -1 {
		fmt.Println("Error: Cannot determine server port for discovery broadcast.")
		return
	}

	ticker := time.NewTicker(BroadcastInterval)
	defer ticker.Stop()

	fmt.Println("Starting discovery broadcast...")

	for {
		select {
		case <-ticker.C:
			msg := types.DiscoveryMessage{
				Type:     types.DiscoveryType,
				ServerIP: localIP,
				Port:     serverPort,
				OS:       runtime.GOOS,
				Hostname: hostname,
			}
			data, err := json.Marshal(msg)
			if err != nil {
				fmt.Printf("Error marshaling discovery message: %v\n", err)
				continue
			}

			_, err = conn.Write(data)
			if err != nil {
				// Don't print error on every tick if network is down
				// fmt.Printf("Error broadcasting discovery message: %v\n", err)
			}
		case <-s.discoveryStop:
			fmt.Println("Stopping discovery broadcast.")
			return
		}
	}
}

// getLocalIP finds a non-loopback, private local IPv4 address
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// Check the address type and if it is not a loopback and is private
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil && ipnet.IP.IsPrivate() { // Check if it's IPv4 and Private
				return ipnet.IP.String(), nil
			}
		}
	}
	// Fallback: Try hostname lookup if interface method fails to find a private IP
	hostname, err := os.Hostname()
	if err == nil {
		ips, err := net.LookupIP(hostname)
		if err == nil {
			for _, ip := range ips {
				if ip.To4() != nil && !ip.IsLoopback() && ip.IsPrivate() {
					return ip.String(), nil
				}
			}
		}
	}
	return "", fmt.Errorf("cannot find suitable private local IP address")
}

// GetListenIP returns the determined local IP address used for discovery.
// It might return "UNKNOWN" if an IP couldn't be determined.
func (s *Server) GetListenIP() string {
	ip, err := getLocalIP()
	if err != nil {
		// Consistent with discovery broadcaster's fallback
		return "UNKNOWN"
	}
	return ip
}

// GetListenPort returns the actual port the server is listening on.
func (s *Server) GetListenPort() int {
	// Check if listener is valid and is a TCPAddr
	if s.listener != nil {
		if tcpAddr, ok := s.listener.Addr().(*net.TCPAddr); ok {
			return tcpAddr.Port
		}
	}
	// Return a default/error indicator if port cannot be determined
	return -1
}

// checkEdgeTransition checks if the physical server mouse position (x, y)
// matches a configured outgoing edge link.
// Returns: switchToClient bool, targetClientAddr string, targetLink types.EdgeLink
func (s *Server) checkEdgeTransition(x, y int) (bool, string, types.EdgeLink) {
	log.Printf("EDGE CHECK: Checking coords (%d, %d)", x, y) // Always log edge checks
	serverScreens := s.GetServerScreens()
	serverHostname, _ := os.Hostname()

	for _, serverScreen := range serverScreens {
		// Ensure detailed logging is active
		log.Printf("Checking (%d,%d) against Screen %d: X[%d -> %d], Y[%d -> %d]",
			x, y, serverScreen.ID, serverScreen.X, serverScreen.X+serverScreen.W, serverScreen.Y, serverScreen.Y+serverScreen.H)

		const edgeBuffer = 10 // Increased buffer from 3 to 10 pixels for better sensitivity
		currentEdge := types.ScreenEdge("")

		// Refined Edge Checks with improved sensitivity:
		// Check Left Edge: X is near left border, Y is within vertical bounds with small buffer
		if x <= serverScreen.X+edgeBuffer && y >= serverScreen.Y-edgeBuffer && y <= serverScreen.Y+serverScreen.H+edgeBuffer {
			currentEdge = types.EdgeLeft
			log.Printf("LEFT EDGE DETECTED at (%d,%d) for screen %d", x, y, serverScreen.ID)
			// Check Right Edge: X is near right border, Y is within vertical bounds with small buffer
		} else if x >= serverScreen.X+serverScreen.W-edgeBuffer && y >= serverScreen.Y-edgeBuffer && y <= serverScreen.Y+serverScreen.H+edgeBuffer {
			currentEdge = types.EdgeRight
			log.Printf("RIGHT EDGE DETECTED at (%d,%d) for screen %d", x, y, serverScreen.ID)
			// Check Top Edge: Y is near top border, X is within horizontal bounds with small buffer
		} else if y <= serverScreen.Y+edgeBuffer && x >= serverScreen.X-edgeBuffer && x <= serverScreen.X+serverScreen.W+edgeBuffer {
			currentEdge = types.EdgeTop
			log.Printf("TOP EDGE DETECTED at (%d,%d) for screen %d", x, y, serverScreen.ID)
			// Check Bottom Edge: Y is near bottom border, X is within horizontal bounds with small buffer
		} else if y >= serverScreen.Y+serverScreen.H-edgeBuffer && x >= serverScreen.X-edgeBuffer && x <= serverScreen.X+serverScreen.W+edgeBuffer {
			currentEdge = types.EdgeBottom
			log.Printf("BOTTOM EDGE DETECTED at (%d,%d) for screen %d", x, y, serverScreen.ID)
		}

		if currentEdge != "" {
			log.Printf("EDGE TRANSITION: Detected edge %s on server screen %d (%s)", currentEdge, serverScreen.ID, serverHostname)
			// Check configurations for all clients for a link FROM this edge
			s.layoutConfigsMutex.RLock()
			for clientAddr, config := range s.layoutConfigs {
				if config == nil {
					continue
				}
				for _, link := range config.Links {
					if link.FromHostname == serverHostname &&
						link.FromScreenID == serverScreen.ID &&
						link.FromEdge == currentEdge {
						log.Printf("EDGE TRANSITION: Found matching outgoing link! Server %d/%s -> Client %s Screen %d/%s",
							serverScreen.ID, currentEdge, link.ToHostname, link.ToScreenID, link.ToEdge)
						s.layoutConfigsMutex.RUnlock()
						return true, clientAddr, link
					}
				}
			}
			s.layoutConfigsMutex.RUnlock()
			log.Printf("EDGE TRANSITION: Detected edge %s on screen %d, but no matching outgoing link found in layout configs.", currentEdge, serverScreen.ID)
		}
		// If loop continues, check next screen (i++)
	}

	return false, "", types.EdgeLink{} // No transition detected on any screen edge
}

// checkReturnTransition checks if the last simulated client position (x, y)
// matches a configured incoming edge link.
// Returns: switchToServer bool, targetServerScreen types.ScreenRect, targetServerEdge types.ScreenEdge
func (s *Server) checkReturnTransition(clientX, clientY int, clientAddr string) (bool, types.ScreenRect, types.ScreenEdge) {
	log.Printf("RETURN CHECK: Checking client coords (%d, %d) for return to server", clientX, clientY)

	s.layoutConfigsMutex.RLock()
	config, configOk := s.layoutConfigs[clientAddr]
	s.layoutConfigsMutex.RUnlock()
	if !configOk || config == nil {
		log.Printf("RETURN CHECK: No config for client %s", clientAddr)
		return false, types.ScreenRect{}, "" // No config for this client
	}

	s.clientsMutex.RLock()
	clientConn, clientOk := s.clients[clientAddr]
	s.clientsMutex.RUnlock()
	if !clientOk || clientConn.MonitorInfo == nil {
		log.Printf("RETURN CHECK: Client disconnected or no monitor info")
		return false, types.ScreenRect{}, "" // Client disconnected or no monitor info
	}

	clientHostname := clientConn.MonitorInfo.Hostname

	for _, clientScreen := range clientConn.MonitorInfo.Screens {
		log.Printf("RETURN CHECK: Checking client screen %d: X[%d -> %d], Y[%d -> %d]",
			clientScreen.ID, clientScreen.X, clientScreen.X+clientScreen.W, clientScreen.Y, clientScreen.Y+clientScreen.H)

		const edgeBuffer = 10 // Increased from 1 to 10 for better sensitivity
		currentEdge := types.ScreenEdge("")

		// Check if the *simulated* client coords are at the edge of *this* client screen
		// Using improved detection similar to checkEdgeTransition
		if clientX <= clientScreen.X+edgeBuffer && clientY >= clientScreen.Y-edgeBuffer && clientY <= clientScreen.Y+clientScreen.H+edgeBuffer {
			currentEdge = types.EdgeLeft
			log.Printf("CLIENT LEFT EDGE DETECTED at (%d,%d) for screen %d", clientX, clientY, clientScreen.ID)
		} else if clientX >= clientScreen.X+clientScreen.W-edgeBuffer && clientY >= clientScreen.Y-edgeBuffer && clientY <= clientScreen.Y+clientScreen.H+edgeBuffer {
			currentEdge = types.EdgeRight
			log.Printf("CLIENT RIGHT EDGE DETECTED at (%d,%d) for screen %d", clientX, clientY, clientScreen.ID)
		} else if clientY <= clientScreen.Y+edgeBuffer && clientX >= clientScreen.X-edgeBuffer && clientX <= clientScreen.X+clientScreen.W+edgeBuffer {
			currentEdge = types.EdgeTop
			log.Printf("CLIENT TOP EDGE DETECTED at (%d,%d) for screen %d", clientX, clientY, clientScreen.ID)
		} else if clientY >= clientScreen.Y+clientScreen.H-edgeBuffer && clientX >= clientScreen.X-edgeBuffer && clientX <= clientScreen.X+clientScreen.W+edgeBuffer {
			currentEdge = types.EdgeBottom
			log.Printf("CLIENT BOTTOM EDGE DETECTED at (%d,%d) for screen %d", clientX, clientY, clientScreen.ID)
		}

		if currentEdge == "" {
			continue
		}

		log.Printf("RETURN CHECK: Detected edge %s on client screen %d", currentEdge, clientScreen.ID)

		// Check the config for links originating from this client screen edge
		for _, link := range config.Links {
			if link.FromHostname == clientHostname &&
				link.FromScreenID == clientScreen.ID &&
				link.FromEdge == currentEdge {

				// Found a link pointing back to the server
				serverHostname, _ := os.Hostname()
				if link.ToHostname == serverHostname {
					// Find the target server screen
					for _, serverScreen := range s.GetServerScreens() {
						if serverScreen.ID == link.ToScreenID {
							log.Printf("RETURN CHECK: Found match! Client %s Screen %d/%s -> Server Screen %d/%s\n",
								clientHostname, clientScreen.ID, currentEdge, serverScreen.ID, link.ToEdge)
							return true, serverScreen, link.ToEdge
						}
					}
				}
			}
		}
	}

	return false, types.ScreenRect{}, ""
}

// Simple helper functions (Go 1.21+ has built-in max/min)
func max[T constraints.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

// calculateEntryPoint calculates the cursor position on the target screen's edge,
// based on the relative position on the source edge.
func calculateEntryPoint(sourceX, sourceY int, sourceScreen types.ScreenRect, sourceEdge types.ScreenEdge,
	targetScreen types.ScreenRect, targetEdge types.ScreenEdge) (targetX, targetY int) {

	var relativePos float64 // Position along the edge (0.0 to 1.0)

	// Calculate relative position on the source edge
	switch sourceEdge {
	case types.EdgeLeft, types.EdgeRight:
		if sourceScreen.H > 0 {
			relativePos = float64(sourceY-sourceScreen.Y) / float64(sourceScreen.H)
		} else {
			relativePos = 0.5 // Avoid division by zero
		}
	case types.EdgeTop, types.EdgeBottom:
		if sourceScreen.W > 0 {
			relativePos = float64(sourceX-sourceScreen.X) / float64(sourceScreen.W)
		} else {
			relativePos = 0.5
		}
	}
	relativePos = math.Max(0.0, math.Min(1.0, relativePos)) // Clamp between 0 and 1

	// Calculate absolute position on the target edge
	const entryOffset = 3 // Pixels inset from the edge
	switch targetEdge {
	case types.EdgeLeft:
		targetX = targetScreen.X + entryOffset
		targetY = targetScreen.Y + int(relativePos*float64(targetScreen.H))
	case types.EdgeRight:
		targetX = targetScreen.X + targetScreen.W - entryOffset
		targetY = targetScreen.Y + int(relativePos*float64(targetScreen.H))
	case types.EdgeTop:
		targetX = targetScreen.X + int(relativePos*float64(targetScreen.W))
		targetY = targetScreen.Y + entryOffset
	case types.EdgeBottom:
		targetX = targetScreen.X + int(relativePos*float64(targetScreen.W))
		targetY = targetScreen.Y + targetScreen.H - entryOffset
	default:
		// Should not happen, default to center of target screen
		targetX = targetScreen.X + targetScreen.W/2
		targetY = targetScreen.Y + targetScreen.H/2
	}

	// Clamp coordinates to be within the target screen bounds (safety)
	targetX = max(targetScreen.X, min(targetX, targetScreen.X+targetScreen.W-1))
	targetY = max(targetScreen.Y, min(targetY, targetScreen.Y+targetScreen.H-1))

	return targetX, targetY
}

// UpdateLayout stores the abstract layout configuration for a given client.
func (s *Server) UpdateLayout(clientAddr string, config *types.LayoutConfiguration) {
	s.layoutConfigsMutex.Lock()
	defer s.layoutConfigsMutex.Unlock()
	log.Printf("Updating layout config for client: %s\n", clientAddr)
	s.layoutConfigs[clientAddr] = config
	// TODO: Persist layout config?
}

// GetLayout retrieves the abstract layout configuration for a given client.
func (s *Server) GetLayout(clientAddr string) (*types.LayoutConfiguration, bool) {
	s.layoutConfigsMutex.RLock()
	defer s.layoutConfigsMutex.RUnlock()
	config, ok := s.layoutConfigs[clientAddr]
	return config, ok
}

// GetServerScreens retrieves the cached server screen info.
func (s *Server) GetServerScreens() []types.ScreenRect {
	// Assuming serverScreens is immutable after startup, no lock needed
	return s.serverScreens
}

// GetWarningChan returns the channel for server warnings.
func (s *Server) GetWarningChan() <-chan string {
	return s.WarningChan
}
