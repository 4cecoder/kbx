package client

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"kb/types"

	"github.com/go-vgo/robotgo"
)

// Removed local type definitions

const (
	DiscoveryAddr = "239.0.0.1:9999"
	DiscoveryType = "KB_SHARE_DISCOVERY_V1"
)

// getLocalMonitorInfo gathers information about the client's monitors
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

type Client struct {
	conn              net.Conn
	encoder           *json.Encoder // Add encoder/decoder
	decoder           *json.Decoder
	stopChan          chan struct{}
	discoveryStop     chan struct{}
	foundServers      map[string]types.DiscoveryMessage
	connectedServer   types.DiscoveryMessage // Info from discovery
	serverMonitorInfo *types.MonitorInfo     // Monitor info received from server
	// Channel to notify UI about received server monitor info?
	// ServerInfoUpdateChan chan *types.MonitorInfo
}

func NewClient() *Client {
	return &Client{
		conn:              nil,
		encoder:           nil,
		decoder:           nil,
		stopChan:          make(chan struct{}),
		discoveryStop:     make(chan struct{}),
		foundServers:      make(map[string]types.DiscoveryMessage),
		serverMonitorInfo: nil,
		// ServerInfoUpdateChan: make(chan *types.MonitorInfo, 1),
	}
}

// sendMessage sends a wrapped message to the server
func (c *Client) sendMessage(msgType types.MessageType, payload interface{}) error {
	if c.encoder == nil {
		return fmt.Errorf("client not connected")
	}
	wrappedMsg := types.WrappedMessage{
		Type:    msgType,
		Payload: payload,
	}
	return c.encoder.Encode(wrappedMsg)
}

func (c *Client) Connect(serverAddr string) error {
	if c.conn != nil {
		c.conn.Close()
	}

	conn, err := net.DialTimeout("tcp", serverAddr, 5*time.Second)
	if err != nil {
		return err
	}
	c.conn = conn
	c.encoder = json.NewEncoder(conn)
	c.decoder = json.NewDecoder(conn)

	if serverInfo, ok := c.foundServers[serverAddr]; ok {
		c.connectedServer = serverInfo
	} else {
		c.connectedServer = types.DiscoveryMessage{ServerIP: serverAddr, Hostname: "Manual Connection"}
	}

	// Send client monitor info immediately after connecting
	clientMonitorInfo := getLocalMonitorInfo()
	err = c.sendMessage(types.TypeMonitorInfo, clientMonitorInfo)
	if err != nil {
		fmt.Printf("Error sending client monitor info: %v\n", err)
		c.conn.Close() // Close connection if initial send fails
		return err
	}

	go c.receiveMessages()
	return nil
}

func (c *Client) Stop() {
	close(c.stopChan)
	close(c.discoveryStop)
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
		c.encoder = nil
		c.decoder = nil
	}
	c.serverMonitorInfo = nil // Clear received info
	// close(c.ServerInfoUpdateChan) // Close update channel
}

// receiveMessages handles incoming wrapped messages from the server
func (c *Client) receiveMessages() {
	defer func() {
		if c.conn != nil {
			c.conn.Close() // Ensure connection is closed on exit
		}
	}()

	for {
		select {
		case <-c.stopChan:
			return
		default:
			if c.decoder == nil {
				// Connection might have been closed, wait briefly
				time.Sleep(100 * time.Millisecond)
				continue
			}
			var wrappedMsg types.WrappedMessage
			err := c.decoder.Decode(&wrappedMsg)
			if err != nil {
				select {
				case <-c.stopChan: // Check if stopping
					return
				default:
					fmt.Printf("Error decoding message from server: %v\n", err)
					return // Exit goroutine on error
				}
			}

			switch wrappedMsg.Type {
			case types.TypeKeyEvent:
				payloadBytes, _ := json.Marshal(wrappedMsg.Payload)
				var event types.KeyEvent
				err := json.Unmarshal(payloadBytes, &event)
				if err != nil {
					fmt.Printf("Error unmarshaling KeyEvent: %v\n", err)
					continue
				}
				// Replay the keyboard event
				if event.Type == "keydown" {
					robotgo.KeyTap(event.Keychar)
					fmt.Printf("Replayed KeyDown: %s\n", event.Keychar)
				} // KeyUp is ignored for now

			case types.TypeMonitorInfo:
				payloadBytes, _ := json.Marshal(wrappedMsg.Payload)
				var monitorInfo types.MonitorInfo
				err := json.Unmarshal(payloadBytes, &monitorInfo)
				if err != nil {
					fmt.Printf("Error unmarshaling MonitorInfo from server: %v\n", err)
					continue
				}
				fmt.Printf("Received Server MonitorInfo: %+v\n", monitorInfo)
				c.serverMonitorInfo = &monitorInfo
				// Notify UI if channel exists
				// select { case c.ServerInfoUpdateChan <- c.serverMonitorInfo: default: }

			case types.TypeMouseEvent:
				payloadBytes, _ := json.Marshal(wrappedMsg.Payload)
				var mouseEvent types.MouseEvent
				err := json.Unmarshal(payloadBytes, &mouseEvent)
				if err != nil {
					fmt.Printf("Error unmarshaling MouseEvent: %v\n", err)
					continue
				}

				// Move the mouse cursor to the coordinates received from the server
				robotgo.Move(mouseEvent.X, mouseEvent.Y)
				fmt.Printf("Moved mouse to X: %d, Y: %d\n", mouseEvent.X, mouseEvent.Y)

			default:
				fmt.Printf("Received unhandled message type '%s' from server\n", wrappedMsg.Type)
			}
		}
	}
}

// StartDiscoveryListener (use types.DiscoveryMessage, otherwise unchanged)
func (c *Client) StartDiscoveryListener() {
	// ... (resolve UDP, listen multicast - unchanged) ...
	addr, err := net.ResolveUDPAddr("udp", DiscoveryAddr)
	if err != nil {
		fmt.Printf("Error resolving UDP address for discovery: %v\n", err)
		return
	}
	listener, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("Error listening for UDP discovery: %v\n", err)
		return
	}
	defer listener.Close()
	listener.SetReadBuffer(1024)

	fmt.Println("Starting discovery listener...")
	buf := make([]byte, 1024)

	for {
		select {
		case <-c.discoveryStop:
			fmt.Println("Stopping discovery listener.")
			return
		default:
			listener.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, _, err := listener.ReadFromUDP(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				select {
				case <-c.discoveryStop:
					return
				default:
					fmt.Printf("Error reading from UDP discovery: %v\n", err)
				}
				continue
			}

			var msg types.DiscoveryMessage // Use type from package
			err = json.Unmarshal(buf[:n], &msg)
			if err != nil {
				continue
			}

			if msg.Type == DiscoveryType {
				serverAddr := fmt.Sprintf("%s:%d", msg.ServerIP, msg.Port)
				c.foundServers[serverAddr] = msg
			}
		}
	}
}

// GetFoundServers (use types.DiscoveryMessage, otherwise unchanged)
func (c *Client) GetFoundServers() map[string]types.DiscoveryMessage {
	return c.foundServers
}

// GetConnectedServerInfo (use types.DiscoveryMessage, otherwise unchanged)
func (c *Client) GetConnectedServerInfo() types.DiscoveryMessage {
	return c.connectedServer
}

// GetReceivedServerMonitorInfo returns the monitor info received from the server.
func (c *Client) GetReceivedServerMonitorInfo() *types.MonitorInfo {
	return c.serverMonitorInfo
}
