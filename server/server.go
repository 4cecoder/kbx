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

func (s *Server) Start() {
	go s.acceptConnections()
	go s.captureKeyboard()
	go s.startDiscoveryBroadcaster()
	go s.trackMouseInput() // Start mouse tracking
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

func (s *Server) captureKeyboard() {
	log.Println("Starting keyboard capture...")
	evChan := hook.Start()
	// Check for Accessibility permission error specifically on macOS
	if runtime.GOOS == "darwin" {
		// A bit hacky: check if the event channel is nil after Start()
		// gohook doesn't seem to return a specific error for permissions,
		// but often fails to initialize the channel.
		// Also check for the known log message text if possible (though fragile).
		// We'll primarily rely on the channel being nil or closed immediately.
		_, chanOpen := <-evChan // Non-blocking read to check if closed
		if !chanOpen {
			log.Println("Warning: hook.Start() failed, likely missing macOS Accessibility permissions.")
			select {
			case s.WarningChan <- "macOS Accessibility permission likely missing.":
			default: // Avoid blocking if channel is full/closed
			}
			// Don't return here; allow hook registration attempt even if check was flaky.
			// If permissions are actually granted, registration should succeed.
			// return
		}
	}
	defer hook.End()

	log.Println("Keyboard capture hook started successfully.")

	hook.Register(hook.KeyDown, []string{}, func(e hook.Event) {
		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		client, clientExists := s.clients[activeAddr]
		s.clientsMutex.RUnlock()

		if isRemote && clientExists { // Only process if input is remote and client exists
			fmt.Printf("[Remote] KeyDown: %s -> %s\n", e.Keychar, activeAddr)
			event := types.KeyEvent{
				Type:    "keydown",
				Keychar: string(e.Keychar),
			}
			// Send only to the active client
			err := s.sendMessage(client, types.TypeKeyEvent, event)
			if err != nil {
				fmt.Printf("Error sending key event to active client %s: %v. Removing client.\n", activeAddr, err)
				go s.removeClient(activeAddr) // Schedule removal
			}
			// TODO: Should we suppress the local event somehow?
			// hook.StopPropagation() // - Check gohook docs if needed/possible
		} else {
			// fmt.Printf("[Local] KeyDown: %s\n", e.Keychar) // Optional debug
		}
	})

	hook.Register(hook.KeyUp, []string{}, func(e hook.Event) {
		s.clientsMutex.RLock()
		isRemote := s.remoteInputActive
		activeAddr := s.activeClientAddr
		client, clientExists := s.clients[activeAddr]
		s.clientsMutex.RUnlock()

		if isRemote && clientExists { // Only process if input is remote and client exists
			fmt.Printf("[Remote] KeyUp: %s -> %s\n", e.Keychar, activeAddr)
			event := types.KeyEvent{
				Type:    "keyup",
				Keychar: string(e.Keychar),
			}
			err := s.sendMessage(client, types.TypeKeyEvent, event)
			if err != nil {
				fmt.Printf("Error sending key event to active client %s: %v. Removing client.\n", activeAddr, err)
				go s.removeClient(activeAddr)
			}
		} else {
			// fmt.Printf("[Local] KeyUp: %s\n", e.Keychar)
		}
	})

	for {
		select {
		case <-s.stopChan:
			return
		case <-evChan:
			// We handle events via the registered callbacks now
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
	serverScreens := s.GetServerScreens()
	serverHostname, _ := os.Hostname()

	for _, serverScreen := range serverScreens {
		const edgeBuffer = 1
		currentEdge := types.ScreenEdge("")

		if x <= serverScreen.X+edgeBuffer && x >= serverScreen.X {
			currentEdge = types.EdgeLeft
		} else if x >= serverScreen.X+serverScreen.W-edgeBuffer && x <= serverScreen.X+serverScreen.W {
			currentEdge = types.EdgeRight
		} else if y <= serverScreen.Y+edgeBuffer && y >= serverScreen.Y {
			currentEdge = types.EdgeTop
		} else if y >= serverScreen.Y+serverScreen.H-edgeBuffer && y <= serverScreen.Y+serverScreen.H {
			currentEdge = types.EdgeBottom
		}

		if currentEdge == "" {
			continue // Not on an edge of this screen
		}

		// Check configurations for all clients
		s.layoutConfigsMutex.RLock()
		for clientAddr, config := range s.layoutConfigs {
			if config == nil {
				continue
			}

			for _, link := range config.Links {
				// Check if the link originates from the current server screen edge
				if link.FromHostname == serverHostname &&
					link.FromScreenID == serverScreen.ID &&
					link.FromEdge == currentEdge {

					// Found a matching outgoing link
					log.Printf("Outgoing edge match: Server %d/%s -> Client %s Screen %d/%s\n",
						serverScreen.ID, currentEdge, link.ToHostname, link.ToScreenID, link.ToEdge)
					s.layoutConfigsMutex.RUnlock()
					return true, clientAddr, link
				}
			}
		}
		s.layoutConfigsMutex.RUnlock()
	}

	return false, "", types.EdgeLink{} // No transition detected
}

// checkReturnTransition checks if the last simulated client position (x, y)
// matches a configured incoming edge link.
// Returns: switchToServer bool, targetServerScreen types.ScreenRect, targetServerEdge types.ScreenEdge
func (s *Server) checkReturnTransition(clientX, clientY int, clientAddr string) (bool, types.ScreenRect, types.ScreenEdge) {
	s.layoutConfigsMutex.RLock()
	config, configOk := s.layoutConfigs[clientAddr]
	s.layoutConfigsMutex.RUnlock()
	if !configOk || config == nil {
		return false, types.ScreenRect{}, "" // No config for this client
	}

	s.clientsMutex.RLock()
	clientConn, clientOk := s.clients[clientAddr]
	s.clientsMutex.RUnlock()
	if !clientOk || clientConn.MonitorInfo == nil {
		return false, types.ScreenRect{}, "" // Client disconnected or no monitor info
	}

	clientHostname := clientConn.MonitorInfo.Hostname

	for _, clientScreen := range clientConn.MonitorInfo.Screens {
		const edgeBuffer = 1
		currentEdge := types.ScreenEdge("")

		// Check if the *simulated* client coords are at the edge of *this* client screen
		if clientX <= clientScreen.X+edgeBuffer && clientX >= clientScreen.X {
			currentEdge = types.EdgeLeft
		} else if clientX >= clientScreen.X+clientScreen.W-edgeBuffer && clientX <= clientScreen.X+clientScreen.W {
			currentEdge = types.EdgeRight
		} else if clientY <= clientScreen.Y+edgeBuffer && clientY >= clientScreen.Y {
			currentEdge = types.EdgeTop
		} else if clientY >= clientScreen.Y+clientScreen.H-edgeBuffer && clientY <= clientScreen.Y+clientScreen.H {
			currentEdge = types.EdgeBottom
		}

		if currentEdge == "" {
			continue
		}

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
							log.Printf("Incoming edge match: Client %s Screen %d/%s -> Server Screen %d/%s\n",
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

func (s *Server) trackMouseInput() {
	log.Println("Starting mouse input tracking...")
	defer log.Println("Stopping mouse input tracking.")

	for {
		select {
		case <-s.stopChan:
			return
		default:
			s.clientsMutex.RLock()
			isRemote := s.remoteInputActive
			activeAddr := s.activeClientAddr
			client, clientExists := s.clients[activeAddr]
			lastX := s.lastSentMouseX
			lastY := s.lastSentMouseY
			s.clientsMutex.RUnlock()

			x, y := robotgo.Location()

			if isRemote && clientExists {
				// --- Currently controlling remote client ---
				switchToServer, targetServerScreen, targetServerEdge := s.checkReturnTransition(lastX, lastY, activeAddr)

				if switchToServer {
					log.Println("Switching input back to server.")
					entryX, entryY := calculateEntryPoint(lastX, lastY,
						client.MonitorInfo.Screens[0], /* TODO: Find correct client screen */
						"unknown",                     /* TODO: Need client exit edge from link */
						targetServerScreen, targetServerEdge)

					s.clientsMutex.Lock()
					s.remoteInputActive = false
					s.activeClientAddr = ""
					s.clientsMutex.Unlock()

					robotgo.Move(entryX, entryY) // Move server cursor to entry point
					continue                     // Skip sending this event to client
				}

				// Send mouse event (use current physical server coords for now)
				// TODO: Consider if client needs relative or absolute coords?
				mouseEvent := types.MouseEvent{X: x, Y: y}
				s.clientsMutex.Lock() // Lock needed to update last sent coords
				s.lastSentMouseX = x
				s.lastSentMouseY = y
				s.clientsMutex.Unlock()

				err := s.sendMessage(client, types.TypeMouseEvent, mouseEvent)
				if err != nil {
					log.Printf("Error sending mouse event to client %s: %v. Removing client.\n", activeAddr, err)
					go s.removeClient(activeAddr)
				}
			} else {
				// --- Currently controlling local server ---
				switchToClient, targetClientAddr, targetLink := s.checkEdgeTransition(x, y)

				if switchToClient {
					log.Printf("Switching input to client: %s\n", targetClientAddr)

					// Find the server screen that was exited
					var sourceScreen types.ScreenRect
					for _, ss := range s.GetServerScreens() {
						if ss.ID == targetLink.FromScreenID {
							sourceScreen = ss
							break
						}
					}

					// Find the target client screen details
					s.clientsMutex.RLock()
					targetClientConn, targetClientOk := s.clients[targetClientAddr]
					s.clientsMutex.RUnlock()
					if !targetClientOk || targetClientConn.MonitorInfo == nil {
						log.Printf("Cannot switch: Target client %s disconnected or has no monitor info.\n", targetClientAddr)
						continue
					}
					var targetClientScreen types.ScreenRect
					for _, cs := range targetClientConn.MonitorInfo.Screens {
						if cs.ID == targetLink.ToScreenID {
							targetClientScreen = cs
							break
						}
					}
					if targetClientScreen.W == 0 { // Check if target screen was found
						log.Printf("Cannot switch: Target client screen ID %d not found for client %s.\n", targetLink.ToScreenID, targetClientAddr)
						continue
					}

					// Calculate initial client cursor position
					initialClientX, initialClientY := calculateEntryPoint(x, y,
						sourceScreen, targetLink.FromEdge,
						targetClientScreen, targetLink.ToEdge)

					// Update state (write lock)
					s.clientsMutex.Lock()
					s.remoteInputActive = true
					s.activeClientAddr = targetClientAddr
					s.lastSentMouseX = initialClientX // Store initial client coords
					s.lastSentMouseY = initialClientY
					activeClient, stillExists := s.clients[targetClientAddr]
					if !stillExists {
						log.Printf("Target client %s disconnected before input switch could complete.", targetClientAddr)
						s.remoteInputActive = false
						s.activeClientAddr = ""
					} else {
						initMouseEvent := types.MouseEvent{X: initialClientX, Y: initialClientY}
						err := s.sendMessage(activeClient, types.TypeMouseEvent, initMouseEvent)
						if err != nil {
							log.Printf("Error sending initial mouse event to client %s: %v. Reverting switch.\n", targetClientAddr, err)
							s.remoteInputActive = false
							s.activeClientAddr = ""
							go s.removeClient(targetClientAddr)
						}
					}
					s.clientsMutex.Unlock()
				}
			}

			time.Sleep(15 * time.Millisecond)
		}
	}
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
