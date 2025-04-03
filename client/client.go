package client

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/go-vgo/robotgo"
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
	DiscoveryAddr = "239.0.0.1:9999" // Must match server
	DiscoveryType = "KB_SHARE_DISCOVERY_V1"
)

type Client struct {
	conn            net.Conn
	stopChan        chan struct{}
	discoveryStop   chan struct{}               // Channel to stop discovery listener
	foundServers    map[string]DiscoveryMessage // Map to store found servers [ip:port] -> message
	connectedServer DiscoveryMessage            // Store info about the server we connected to
}

// NewClient creates a client instance but doesn't connect yet.
// It primarily initializes structures needed for discovery.
func NewClient() *Client {
	return &Client{
		conn:          nil, // Connection established later
		stopChan:      make(chan struct{}),
		discoveryStop: make(chan struct{}),
		foundServers:  make(map[string]DiscoveryMessage),
	}
}

// Connect dials the specified server address.
func (c *Client) Connect(serverAddr string) error {
	// Close existing connection if any
	if c.conn != nil {
		c.conn.Close()
	}

	conn, err := net.DialTimeout("tcp", serverAddr, 5*time.Second) // Add timeout
	if err != nil {
		return err
	}
	c.conn = conn

	// If we found this server via discovery, store its info
	if serverInfo, ok := c.foundServers[serverAddr]; ok {
		c.connectedServer = serverInfo
	} else {
		// TODO: Maybe implement a handshake to get server info if connected manually?
		c.connectedServer = DiscoveryMessage{ServerIP: serverAddr, Hostname: "Manual Connection"} // Placeholder
	}

	go c.receiveEvents()
	return nil
}

func (c *Client) Stop() {
	close(c.stopChan)      // Signal receiver to stop
	close(c.discoveryStop) // Signal discovery listener to stop
	if c.conn != nil {     // Check if conn was initialized
		c.conn.Close()
		c.conn = nil // Ensure connection is marked as closed
	}
	// Clear found servers maybe? Or keep them for next time?
	// c.foundServers = make(map[string]DiscoveryMessage)
}

func (c *Client) receiveEvents() {
	decoder := json.NewDecoder(c.conn)
	for {
		select {
		case <-c.stopChan:
			return
		default:
			var event KeyEvent
			err := decoder.Decode(&event)
			if err != nil {
				fmt.Printf("Error decoding event: %v\n", err)
				return
			}

			// Replay the keyboard event
			if event.Type == "keydown" {
				robotgo.KeyTap(event.Keychar)
				fmt.Printf("Replayed KeyDown: %s\n", event.Keychar)
			} else if event.Type == "keyup" {
				fmt.Printf("Received KeyUp: %s (usually auto-handled)\n", event.Keychar)
			}
		}
	}
}

// StartDiscoveryListener listens for server broadcast messages
func (c *Client) StartDiscoveryListener() {
	addr, err := net.ResolveUDPAddr("udp", DiscoveryAddr)
	if err != nil {
		fmt.Printf("Error resolving UDP address for discovery: %v\n", err)
		return
	}

	// Listen on all interfaces for multicast
	listener, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("Error listening for UDP discovery: %v\n", err)
		return
	}
	defer listener.Close()
	listener.SetReadBuffer(1024) // Set a reasonable buffer size

	fmt.Println("Starting discovery listener...")
	buf := make([]byte, 1024)

	for {
		select {
		case <-c.discoveryStop:
			fmt.Println("Stopping discovery listener.")
			return
		default:
			// Set a deadline to allow checking the stop channel
			listener.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, _, err := listener.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // It's just a timeout, loop again
				}
				// Don't spam errors if the listener is closed intentionally
				select {
				case <-c.discoveryStop:
					return
				default:
					fmt.Printf("Error reading from UDP discovery: %v\n", err)
				}
				continue
			}

			var msg DiscoveryMessage
			err = json.Unmarshal(buf[:n], &msg)
			if err != nil {
				// fmt.Printf("Error unmarshaling discovery message: %v\n", err)
				continue
			}

			if msg.Type == DiscoveryType {
				serverAddr := fmt.Sprintf("%s:%d", msg.ServerIP, msg.Port)
				c.foundServers[serverAddr] = msg
				// Optional: Notify the UI about the found server
				// fmt.Printf("Discovered Server: %s (%s - %s)\n", serverAddr, msg.Hostname, msg.OS)
			}
		}
	}
}

// GetFoundServers returns the list of discovered servers
func (c *Client) GetFoundServers() map[string]DiscoveryMessage {
	// Return a copy to avoid race conditions if UI modifies it?
	// For now, direct access is simpler but be mindful.
	return c.foundServers
}

// GetConnectedServerInfo returns info about the server we are connected to.
func (c *Client) GetConnectedServerInfo() DiscoveryMessage {
	return c.connectedServer
}
