package server

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	hook "github.com/robotn/gohook"
)

type KeyEvent struct {
	Type    string `json:"type"`
	Keychar string `json:"keychar"`
	State   string `json:"state"`
}

// DiscoveryMessage defines the structure for UDP broadcast messages
type DiscoveryMessage struct {
	Type     string `json:"type"`
	ServerIP string `json:"server_ip"`
	Port     int    `json:"port"`
	OS       string `json:"os"`
	Hostname string `json:"hostname"`
}

const (
	DiscoveryAddr    = "239.0.0.1:9999" // Example multicast address
	DiscoveryType    = "KB_SHARE_DISCOVERY_V1"
	BroadcastInterval = 5 * time.Second
)

type Server struct {
	listener net.Listener
	clients  []net.Conn
	stopChan chan struct{}
	discoveryStop chan struct{} // Channel to stop discovery broadcast
}

func NewServer(port int) (*Server, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}

	return &Server{
		listener: listener,
		clients:  make([]net.Conn, 0),
		stopChan:       make(chan struct{}),
		discoveryStop: make(chan struct{}),
	}, nil
}

func (s *Server) Start() {
	go s.acceptConnections()
	go s.captureKeyboard()
	go s.startDiscoveryBroadcaster() // Start broadcasting presence
}

func (s *Server) Stop() {
	close(s.stopChan)
	close(s.discoveryStop) // Signal discovery broadcast to stop
	s.listener.Close()
	for _, client := range s.clients {
		client.Close()
	}
}

func (s *Server) acceptConnections() {
	for {
		select {
		case <-s.stopChan:
			return
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				fmt.Printf("Error accepting connection: %v\n", err)
				continue
			}
			s.clients = append(s.clients, conn)
			// Store client IP address if needed for identification
			// clientAddr := conn.RemoteAddr().String()
			fmt.Printf("New client connected: %s\n", conn.RemoteAddr().String())
		}
	}
}

func (s *Server) captureKeyboard() {
	hook.Register(hook.KeyDown, []string{}, func(e hook.Event) {
		fmt.Printf("KeyDown: %s\n", e.Keychar)
		event := KeyEvent{
			Type:    "keydown",
			Keychar: string(e.Keychar),
			State:   "down",
		}
		s.broadcastEvent(event)
	})

	hook.Register(hook.KeyUp, []string{}, func(e hook.Event) {
		fmt.Printf("KeyUp: %s\n", e.Keychar)
		event := KeyEvent{
			Type:    "keyup",
			Keychar: string(e.Keychar),
			State:   "up",
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

func (s *Server) broadcastEvent(event KeyEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		fmt.Printf("Error marshaling event: %v\n", err)
		return
	}

	// Remove disconnected clients
	activeClients := make([]net.Conn, 0)
	for _, client := range s.clients {
		_, err := client.Write(data)
		if err != nil {
			client.Close()
			continue
		}
		activeClients = append(activeClients, client)
	}
	s.clients = activeClients
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
	serverPort := s.listener.Addr().(*net.TCPAddr).Port

	ticker := time.NewTicker(BroadcastInterval)
	defer ticker.Stop()

	fmt.Println("Starting discovery broadcast...")

	for {
		select {
		case <-ticker.C:
			msg := DiscoveryMessage{
				Type:     DiscoveryType,
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

// getLocalIP finds a non-loopback local IP address
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}
	for _, address := range addrs {
		// Check the address type and if it is not a loopback
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	// Attempt to get hostname IP if interface method fails
	hostname, err := os.Hostname()
	if err == nil {
		ips, err := net.LookupIP(hostname)
		if err == nil {
			for _, ip := range ips {
				if ip.To4() != nil && !ip.IsLoopback() {
					return ip.String(), nil
				}
			}
		}
	}
	return "", fmt.Errorf("cannot find suitable local IP address")
} 