package types

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

// MessageType differentiates messages sent over TCP
type MessageType string

const (
	DiscoveryType               = "KB_SHARE_DISCOVERY_V1"
	TypeKeyEvent    MessageType = "key_event"
	TypeMonitorInfo MessageType = "monitor_info"
	// Add other types later if needed (e.g., mouse events, config updates)
)

// WrappedMessage is a generic container for sending typed messages over TCP
type WrappedMessage struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload"` // Can be KeyEvent, MonitorInfo, etc.
}
