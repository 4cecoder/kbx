package client

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
	encoder           *json.Encoder
	decoder           *json.Decoder
	discoveryConn     net.PacketConn
	foundServers      map[string]types.DiscoveryMessage // Key: Display string
	foundServersMtx   sync.RWMutex
	stopListenerChan  chan struct{}
	connectedServer   types.DiscoveryMessage // Info about the currently connected server
	serverMonitorInfo *types.MonitorInfo     // Monitor info received from server
	// Channel to notify UI about received server monitor info?
	// ServerInfoUpdateChan chan *types.MonitorInfo

	// Auto-reconnect fields
	LastServerAddr  string        // Store the address for reconnection attempts (Exported)
	reconnectTicker *time.Ticker  // Ticker for reconnection attempts
	stopReconnect   chan struct{} // Channel to signal stopping reconnection
	reconnectMutex  sync.Mutex    // Mutex to protect reconnect state changes
	isReconnecting  bool          // Flag indicating if reconnect loop is active
}

func NewClient() *Client {
	return &Client{
		conn:              nil,
		encoder:           nil,
		decoder:           nil,
		discoveryConn:     nil,
		foundServers:      make(map[string]types.DiscoveryMessage),
		foundServersMtx:   sync.RWMutex{},
		stopListenerChan:  make(chan struct{}),
		connectedServer:   types.DiscoveryMessage{},
		serverMonitorInfo: nil,
		// ServerInfoUpdateChan: make(chan *types.MonitorInfo, 1),
		LastServerAddr:  "",
		reconnectTicker: nil,
		stopReconnect:   make(chan struct{}),
		reconnectMutex:  sync.Mutex{},
		isReconnecting:  false,
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

// isValidServerAddress checks if the address string is in host:port format.
func isValidServerAddress(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	return err == nil && host != "" && port != ""
}

// StopReconnectIfNeeded stops the reconnection ticker and loop if it's running. (Exported)
func (c *Client) StopReconnectIfNeeded() {
	c.reconnectMutex.Lock()
	defer c.reconnectMutex.Unlock()

	if c.isReconnecting {
		log.Println("Stopping previous reconnect attempt.")
		if c.reconnectTicker != nil {
			c.reconnectTicker.Stop()
			c.reconnectTicker = nil
		}
		// Close channel non-blockingly to signal loop to stop
		select {
		case <-c.stopReconnect:
			// Already closed
		default:
			close(c.stopReconnect)
		}
		c.isReconnecting = false
	}
}

func (c *Client) Connect(serverAddr string) error {
	if c.conn != nil {
		c.conn.Close()
	}

	if !isValidServerAddress(serverAddr) {
		return fmt.Errorf("invalid server address format: %s", serverAddr)
	}

	c.StopReconnectIfNeeded() // Stop any previous reconnect attempts

	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		return err
	}
	c.conn = conn
	c.encoder = json.NewEncoder(conn)
	c.decoder = json.NewDecoder(conn)

	// Store connection info for potential reconnect
	c.LastServerAddr = serverAddr
	// Find and store the full DiscoveryMessage if found via discovery
	c.foundServersMtx.RLock()
	for _, msg := range c.foundServers {
		if fmt.Sprintf("%s:%d", msg.ServerIP, msg.Port) == serverAddr {
			c.connectedServer = msg
			break
		}
	}
	c.foundServersMtx.RUnlock()
	// TODO: If connected manually, connectedServer might be incomplete.

	// Send client monitor info immediately after connecting
	clientMonitorInfo := getLocalMonitorInfo()
	err = c.sendMessage(types.TypeMonitorInfo, clientMonitorInfo)
	if err != nil {
		fmt.Printf("Error sending client monitor info: %v\n", err)
		c.conn.Close() // Close connection if initial send fails
		return err
	}

	// Start message listener goroutine
	go c.receiveMessages()

	return nil
}

func (c *Client) Stop() {
	close(c.stopListenerChan)
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
		case <-c.stopListenerChan:
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
				case <-c.stopListenerChan: // Check if stopping
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
		case <-c.stopListenerChan:
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
				case <-c.stopListenerChan:
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

// listenForMessages handles incoming messages from the server.
func (c *Client) listenForMessages() {
	log.Println("Client started listening for messages...")
	for {
		var wrappedMsg types.WrappedMessage
		decodeErr := c.decoder.Decode(&wrappedMsg) // Capture error here

		// --- Error Handling ---
		if decodeErr != nil {
			select {
			case <-c.stopListenerChan: // Check if stopping deliberately
				log.Println("Client stopping listener.")
				return
			default:
				// Connection likely dropped
				log.Printf("Error receiving message from server: %v. Attempting reconnect...", decodeErr) // Use captured error
				// Close the current (broken) connection
				if c.conn != nil {
					c.conn.Close()
					c.conn = nil // Set conn to nil to indicate disconnected state
				}
				log.Println("Calling startReconnectLoop from listenForMessages...") // Add log
				c.startReconnectLoop()                                              // Initiate reconnection
				return                                                              // Exit the current listening loop
			}
		}

		// Process message if no error occurred
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

// startReconnectLoop starts a ticker to attempt reconnection.
func (c *Client) startReconnectLoop() {
	log.Println("Entered startReconnectLoop") // Add log
	c.reconnectMutex.Lock()
	if c.isReconnecting {
		log.Println("Reconnect loop already running.")
		c.reconnectMutex.Unlock()
		return
	}
	if c.LastServerAddr == "" {
		log.Println("Cannot start reconnect loop: last server address unknown.")
		c.reconnectMutex.Unlock()
		return
	}

	// Reset stop channel for this new loop
	c.stopReconnect = make(chan struct{})
	c.isReconnecting = true
	c.reconnectTicker = time.NewTicker(10 * time.Second)
	addrToReconnect := c.LastServerAddr // Capture exported address for the goroutine
	log.Printf("Starting reconnect loop for %s...", addrToReconnect)
	c.reconnectMutex.Unlock()

	go func() {
		log.Println("Reconnect goroutine started.") // Add log
		defer func() {
			c.reconnectMutex.Lock()
			if c.reconnectTicker != nil {
				c.reconnectTicker.Stop()
				c.reconnectTicker = nil
			}
			c.isReconnecting = false
			log.Println("Reconnect loop finished.")
			c.reconnectMutex.Unlock()
		}()

		// Attempt immediate reconnect first
		log.Printf("Attempting initial reconnect to %s...", addrToReconnect)
		err := c.Connect(addrToReconnect) // Call the main Connect method
		if err == nil {
			log.Println("Reconnect successful.")
			return // Success, exit goroutine
		}
		log.Printf("Initial reconnect failed: %v", err)

		// Start ticker loop
		for {
			select {
			case <-c.reconnectTicker.C:
				log.Printf("Attempting reconnect to %s...", addrToReconnect)
				err := c.Connect(addrToReconnect) // Call main Connect
				if err == nil {
					log.Println("Reconnect successful.")
					return // Success, exit goroutine
				}
				log.Printf("Reconnect attempt failed: %v", err)
			case <-c.stopReconnect:
				log.Println("Received stop signal for reconnect loop.")
				return // Exit goroutine
			}
		}
	}()
}

// IsConnected checks if the client currently has an active connection.
func (c *Client) IsConnected() bool {
	c.reconnectMutex.Lock() // Use reconnect mutex for consistency, though conn check might be atomic
	defer c.reconnectMutex.Unlock()
	return c.conn != nil
}

// IsReconnecting checks if the client is currently in the reconnect loop.
func (c *Client) IsReconnecting() bool {
	c.reconnectMutex.Lock()
	defer c.reconnectMutex.Unlock()
	return c.isReconnecting
}
