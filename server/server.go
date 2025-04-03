package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	"kb/types"

	"github.com/go-vgo/robotgo"
	hook "github.com/robotn/gohook"
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

	// Input Redirection State
	remoteInputActive bool   // Is input currently directed to a client?
	activeClientAddr  string // Which client address is receiving input?

	// Layout Management
	virtualLayouts      map[string]map[string]*types.VirtualScreen // map[clientAddr][screenKey]*VirtualScreen
	virtualLayoutsMutex sync.RWMutex                               // Use RWMutex
	serverScreens       []types.ScreenRect                         // Cache server screens for faster access
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
		remoteInputActive: false,
		activeClientAddr:  "",
		virtualLayouts:    make(map[string]map[string]*types.VirtualScreen),
		serverScreens:     getLocalMonitorInfo().Screens, // Cache server screens on startup
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

	// Remove layout info for the disconnected client
	s.virtualLayoutsMutex.Lock()
	delete(s.virtualLayouts, addr)
	s.virtualLayoutsMutex.Unlock()

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

	evh := hook.Start()
	defer hook.End()

	for {
		select {
		case <-s.stopChan:
			return
		case <-evh:
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

// checkEdgeTransition checks if the current mouse position triggers a transition
// to a client screen based on the virtual layout.
// Returns: switchToClient bool, targetClientAddr string, targetScreen *types.VirtualScreen
// NOTE: Coordinate mapping logic is currently placeholder and needs implementation.
func (s *Server) checkEdgeTransition(x, y int) (bool, string, *types.VirtualScreen) {
	serverScreens := s.GetServerScreens()

	for _, serverScreen := range serverScreens {
		const edgeBuffer = 1

		onLeftEdge := x <= serverScreen.X+edgeBuffer && x >= serverScreen.X
		onRightEdge := x >= serverScreen.X+serverScreen.W-edgeBuffer && x <= serverScreen.X+serverScreen.W
		onTopEdge := y <= serverScreen.Y+edgeBuffer && y >= serverScreen.Y
		onBottomEdge := y >= serverScreen.Y+serverScreen.H-edgeBuffer && y <= serverScreen.Y+serverScreen.H

		if !(onLeftEdge || onRightEdge || onTopEdge || onBottomEdge) {
			continue
		}

		s.virtualLayoutsMutex.RLock()
		for clientAddr, layout := range s.virtualLayouts {
			if layout == nil {
				continue
			}

			for _, virtualScreen := range layout {
				if virtualScreen.IsServer {
					continue
				}

				vX := virtualScreen.Position.X
				vY := virtualScreen.Position.Y
				vW := virtualScreen.Size.Width
				vH := virtualScreen.Size.Height

				// We need a way to get the corresponding virtual position for the server screen
				serverHostname, _ := os.Hostname()                                  // Assuming server hostname is needed for key
				serverScreenKey := types.ScreenKey(serverHostname, serverScreen.ID) // Use types.ScreenKey
				virtualServerScreen, ok := layout[serverScreenKey]
				if !ok {
					continue
				}
				vServerX := virtualServerScreen.Position.X
				vServerY := virtualServerScreen.Position.Y
				vServerW := virtualServerScreen.Size.Width
				vServerH := virtualServerScreen.Size.Height

				adjacent := false
				tolerance := float32(5.0) // Adjacency tolerance
				if onRightEdge && vX > vServerX && (vX-(vServerX+vServerW)) < tolerance {
					if max(vServerY, vY) < min(vServerY+vServerH, vY+vH) {
						adjacent = true
					}
				} else if onLeftEdge && (vX+vW) < vServerX && (vServerX-(vX+vW)) < tolerance {
					if max(vServerY, vY) < min(vServerY+vServerH, vY+vH) {
						adjacent = true
					}
				} else if onBottomEdge && vY > vServerY && (vY-(vServerY+vServerH)) < tolerance {
					if max(vServerX, vX) < min(vServerX+vServerW, vX+vW) {
						adjacent = true
					}
				} else if onTopEdge && (vY+vH) < vServerY && (vServerY-(vY+vH)) < tolerance {
					if max(vServerX, vX) < min(vServerX+vServerW, vX+vW) {
						adjacent = true
					}
				}

				if adjacent {
					log.Printf("Edge transition detected: Server screen %d to Client %s screen %d\n", serverScreen.ID, clientAddr, virtualScreen.ID)
					s.virtualLayoutsMutex.RUnlock()
					return true, clientAddr, virtualScreen
				}
			}
		}
		s.virtualLayoutsMutex.RUnlock()
	}

	return false, "", nil
}

// trackMouseInput continuously monitors the mouse and sends events or checks for transitions.
func (s *Server) trackMouseInput() {
	log.Println("Starting mouse input tracking...")
	defer log.Println("Stopping mouse input tracking.")

	for {
		select {
		case <-s.stopChan:
			return
		default:
			// Get state (read lock)
			s.clientsMutex.RLock()
			isRemote := s.remoteInputActive
			activeAddr := s.activeClientAddr
			client, clientExists := s.clients[activeAddr]
			s.clientsMutex.RUnlock()

			// Get current mouse position
			x, y := robotgo.Location()

			if isRemote && clientExists {
				// --- Currently controlling remote client ---

				// TODO: Check if position indicates transition *back* to server.

				// Send mouse event
				mouseEvent := types.MouseEvent{X: x, Y: y}
				err := s.sendMessage(client, types.TypeMouseEvent, mouseEvent)
				if err != nil {
					log.Printf("Error sending mouse event to client %s: %v. Removing client.\n", activeAddr, err)
					go s.removeClient(activeAddr)
				}
			} else {
				// --- Currently controlling local server ---

				// Check if mouse is at an edge bordering a client
				switchToClient, targetClientAddr, targetScreen := s.checkEdgeTransition(x, y)

				if switchToClient {
					log.Printf("Switching input to client: %s\n", targetClientAddr)

					// TODO: Calculate initial client cursor position (Placeholder)
					initialClientX := targetScreen.Original.X + 5
					initialClientY := targetScreen.Original.Y + 5

					// Update state (write lock)
					s.clientsMutex.Lock()
					s.remoteInputActive = true
					s.activeClientAddr = targetClientAddr
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

// UpdateLayout stores the virtual screen layout for a given client.
// This would be called by the UI after a drag operation (via a channel or method).
// For now, we assume the UI package will call this.
func (s *Server) UpdateLayout(clientAddr string, layout map[string]*types.VirtualScreen) {
	s.virtualLayoutsMutex.Lock()
	defer s.virtualLayoutsMutex.Unlock()
	log.Printf("Updating layout for client: %s\n", clientAddr)
	s.virtualLayouts[clientAddr] = layout
	// TODO: Persist layout?
}

// GetLayout retrieves the virtual screen layout for a given client.
func (s *Server) GetLayout(clientAddr string) (map[string]*types.VirtualScreen, bool) {
	s.virtualLayoutsMutex.RLock()
	defer s.virtualLayoutsMutex.RUnlock()
	layout, ok := s.virtualLayouts[clientAddr]
	// Return a copy to prevent modification?
	// For now, return direct map, but be careful.
	return layout, ok
}

// GetServerScreens retrieves the cached server screen info.
func (s *Server) GetServerScreens() []types.ScreenRect {
	// Assuming serverScreens is immutable after startup, no lock needed
	return s.serverScreens
}
