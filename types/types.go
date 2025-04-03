package types

import "fmt"

// ScreenRect holds information about a single screen
type ScreenRect struct {
	ID int `json:"id"`
	X  int `json:"x"`
	Y  int `json:"y"`
	W  int `json:"w"`
	H  int `json:"h"`
}

// MonitorInfo holds details about a machine's monitor setup and OS
type MonitorInfo struct {
	Hostname string       `json:"hostname"`
	OS       string       `json:"os"`
	Screens  []ScreenRect `json:"screens"`
}

// KeyEvent represents a keyboard action (remains the same, moved here)
type KeyEvent struct {
	Type    string `json:"type"`    // "keydown" or "keyup"
	Keychar string `json:"keychar"` // Character representation
	// State   string `json:"state"`   // "down" or "up" - Redundant with Type? Remove for now.
}

// DiscoveryMessage defines the structure for UDP broadcast messages (remains the same, moved here)
type DiscoveryMessage struct {
	Type     string `json:"type"`
	ServerIP string `json:"server_ip"`
	Port     int    `json:"port"`
	OS       string `json:"os"`
	Hostname string `json:"hostname"`
}

// MouseEvent represents a mouse action
type MouseEvent struct {
	X int `json:"x"`
	Y int `json:"y"`
	// Add other fields later: Button (left, right, middle), Action (click, down, up), ScrollX, ScrollY
}

// VirtualScreen represents a screen in the combined virtual layout
// Used by the UI and potentially the server logic for edge detection
type VirtualScreen struct {
	ID       int        `json:"id"`
	Hostname string     `json:"hostname"`
	IsServer bool       `json:"is_server"`
	Original ScreenRect `json:"original"` // Original dimensions/coords
	// Widget field removed - specific to UI, not a shared type
	Position Position `json:"position"` // Current position in the layout window (scaled)
	Size     Size     `json:"size"`     // Current size in the layout window (scaled)
}

// Position defines simple X, Y (needed by VirtualScreen if fyne types aren't used here)
type Position struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
}

// Size defines simple W, H (needed by VirtualScreen if fyne types aren't used here)
type Size struct {
	Width  float32 `json:"width"`
	Height float32 `json:"height"`
}

// MessageType differentiates messages sent over TCP
type MessageType string

const (
	DiscoveryType               = "KB_SHARE_DISCOVERY_V1"
	TypeKeyEvent    MessageType = "key_event"
	TypeMonitorInfo MessageType = "monitor_info"
	TypeMouseEvent  MessageType = "mouse_event"
	// Add other types later if needed (e.g., mouse events, config updates)
)

// WrappedMessage is a generic container for sending typed messages over TCP
type WrappedMessage struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload"` // Can be KeyEvent, MonitorInfo, etc.
}

// ScreenEdge represents the edges of a screen for linking layouts
type ScreenEdge string

const (
	EdgeLeft   ScreenEdge = "left"
	EdgeRight  ScreenEdge = "right"
	EdgeTop    ScreenEdge = "top"
	EdgeBottom ScreenEdge = "bottom"
)

// EdgeLink defines a connection between two screen edges in the layout
type EdgeLink struct {
	FromHostname string     `json:"from_hostname"`
	FromScreenID int        `json:"from_screen_id"`
	FromEdge     ScreenEdge `json:"from_edge"`
	ToHostname   string     `json:"to_hostname"`
	ToScreenID   int        `json:"to_screen_id"`
	ToEdge       ScreenEdge `json:"to_edge"`
}

// LayoutConfiguration stores the defined links between screens for a specific client
type LayoutConfiguration struct {
	Links []EdgeLink `json:"links"`
}

// Helper to create a unique key for a screen
func ScreenKey(hostname string, screenID int) string {
	return fmt.Sprintf("%s-%d", hostname, screenID)
}
