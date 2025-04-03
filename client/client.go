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
	discoveryStop   chan struct{} // Channel to stop discovery listener
	foundServers    map[string]DiscoveryMessage // Map to store found servers [ip:port] -> message
}

func NewClient(serverAddr string) (*Client, error) {
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return nil, err
	}

	return &Client{
		conn:            conn,
		stopChan:        make(chan struct{}),
		discoveryStop:   make(chan struct{}),
		foundServers:    make(map[string]DiscoveryMessage),
	}, nil
}

func (c *Client) Start() {
	go c.receiveEvents()
}

func (c *Client) Stop() {
	close(c.stopChan)
	close(c.discoveryStop) // Signal discovery listener to stop
	if c.conn != nil {      // Check if conn was initialized
		c.conn.Close()
	}
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
	return c.foundServers
} 