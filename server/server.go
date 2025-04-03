package server

import (
	"encoding/json"
	"fmt"
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

	return &Server{
		listener:         listener,
		clients:          make(map[string]*ClientConnection),
		stopChan:         make(chan struct{}),
		discoveryStop:    make(chan struct{}),
		ClientUpdateChan: make(chan *ClientConnection, 5), // Buffered channel for UI updates
	}, nil
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
		fmt.Printf("Client disconnected: %s\n", addr)
	}
	s.clientsMutex.Unlock()
	// Optionally send another update to UI? Maybe not needed if UI polls.
}

func (s *Server) captureKeyboard() {
	hook.Register(hook.KeyDown, []string{}, func(e hook.Event) {
		fmt.Printf("KeyDown: %s\n", e.Keychar)
		event := types.KeyEvent{
			Type:    "keydown",
			Keychar: string(e.Keychar),
		}
		s.broadcastEvent(event)
	})

	hook.Register(hook.KeyUp, []string{}, func(e hook.Event) {
		fmt.Printf("KeyUp: %s\n", e.Keychar)
		event := types.KeyEvent{
			Type:    "keyup",
			Keychar: string(e.Keychar),
		}
		s.broadcastEvent(event)
	})

	evh := hook.Start()
	defer hook.End()

	for {
		select {
		case <-s.stopChan:
			return
		case <-evh:
		}
	}
}

// broadcastEvent sends an event to all connected clients
func (s *Server) broadcastEvent(event types.KeyEvent) {
	s.clientsMutex.RLock() // Use RLock for reading the map
	defer s.clientsMutex.RUnlock()

	if len(s.clients) == 0 {
		return // No clients to send to
	}

	for addr, client := range s.clients {
		err := s.sendMessage(client, types.TypeKeyEvent, event)
		if err != nil {
			fmt.Printf("Error sending event to client %s: %v. Removing client.\n", addr, err)
			// Need to handle removal carefully - maybe schedule removal outside RLock
			go s.removeClient(addr) // Remove client in a separate goroutine
		}
	}
}

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
