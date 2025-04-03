package main

import (
	"fmt"
	"image/color"

	"kb/types"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// monitorWidget represents a draggable screen in the layout UI
type monitorWidget struct {
	widget.BaseWidget
	screenInfo types.ScreenRect
	hostname   string
	bgColor    color.Color

	label *widget.Label
	rect  *canvas.Rectangle
	cont  *fyne.Container

	// Dragging state
	dragStart    fyne.Position
	lastDragPos  fyne.Position
	parentOffset fyne.Position // Offset within the parent layout container

	// Callback
	OnDragEnd func(m *monitorWidget)
}

// NewMonitorWidget creates a new monitor widget
func NewMonitorWidget(screen types.ScreenRect, hostname string, isServer bool) *monitorWidget {
	m := &monitorWidget{
		screenInfo:   screen,
		hostname:     hostname,
		parentOffset: fyne.NewPos(0, 0), // Initial offset
	}

	if isServer {
		m.bgColor = color.NRGBA{R: 180, G: 220, B: 255, A: 150} // Light blue for server
	} else {
		m.bgColor = color.NRGBA{R: 255, G: 220, B: 180, A: 150} // Light orange for client
	}

	m.ExtendBaseWidget(m)
	return m
}

// CreateRenderer is a standard Fyne method
func (m *monitorWidget) CreateRenderer() fyne.WidgetRenderer {
	m.label = widget.NewLabel(fmt.Sprintf("%s\nID: %d\n%dx%d", m.hostname, m.screenInfo.ID, m.screenInfo.W, m.screenInfo.H))
	m.label.Alignment = fyne.TextAlignCenter
	m.rect = canvas.NewRectangle(m.bgColor)
	m.cont = container.NewStack(m.rect, m.label) // Stack label on top of rect
	return &monitorWidgetRenderer{widget: m}
}

// --- Draggable Implementation ---

// Dragged is called when the widget is dragged
func (m *monitorWidget) Dragged(e *fyne.DragEvent) {
	// Calculate the difference from the last drag position
	delta := e.Position.Subtract(m.lastDragPos)
	m.parentOffset = m.parentOffset.Add(delta) // Update the offset within the parent

	// Move the widget itself (its container)
	// We move our internal container relative to our BaseWidget position,
	// but the parent container will handle the actual widget position.
	// For absolute positioning, we need the parent to move us based on m.parentOffset.
	m.Move(m.Position().Add(delta)) // This might be redundant if parent handles move

	m.lastDragPos = e.Position
	m.Refresh() // Refresh to show potential visual changes

	// Notify parent container (if needed) - Requires callback or interface
	// fmt.Printf("Dragged %s ID %d to offset %v\n", m.hostname, m.screenInfo.ID, m.parentOffset)
}

// DragEnd is called when dragging stops
func (m *monitorWidget) DragEnd() {
	// Here we would calculate snapping or finalize position
	fmt.Printf("Internal DragEnd %s ID %d at offset %v\n", m.hostname, m.screenInfo.ID, m.parentOffset)
	// Call the callback if it's set
	if m.OnDragEnd != nil {
		m.OnDragEnd(m)
	}
}

// Tapped is needed for draggable in some contexts, might not be used
func (m *monitorWidget) Tapped(e *fyne.PointEvent) {}

// TappedSecondary is needed for draggable in some contexts
func (m *monitorWidget) TappedSecondary(e *fyne.PointEvent) {}

// MouseDown is used to capture the start position for dragging calculations
// This requires the driver/desktop import
func (m *monitorWidget) MouseDown(e *desktop.MouseEvent) {
	m.dragStart = e.Position
	m.lastDragPos = e.Position
	// Record initial offset if needed, might be simpler to do in Dragged
}

// MouseUp is not strictly required by Draggable but useful
func (m *monitorWidget) MouseUp(e *desktop.MouseEvent) {}

// --- Custom Methods ---

// GetOffset returns the current offset within the parent container
func (m *monitorWidget) GetOffset() fyne.Position {
	return m.parentOffset
}

// SetOffset sets the initial offset (used by the layout container)
func (m *monitorWidget) SetOffset(pos fyne.Position) {
	m.parentOffset = pos
	// m.Move(pos) // Let the parent layout handle the move
	m.Refresh()
}

// --- Renderer ---

type monitorWidgetRenderer struct {
	widget *monitorWidget
}

func (r *monitorWidgetRenderer) Layout(size fyne.Size) {
	r.widget.cont.Resize(size)
}

func (r *monitorWidgetRenderer) MinSize() fyne.Size {
	// Calculate a reasonable minimum size based on label?
	minLabel := r.widget.label.MinSize()
	// Add some padding
	return fyne.NewSize(minLabel.Width+20, minLabel.Height+20)
}

func (r *monitorWidgetRenderer) Refresh() {
	r.widget.label.SetText(fmt.Sprintf("%s\nID: %d\n%dx%d", r.widget.hostname, r.widget.screenInfo.ID, r.widget.screenInfo.W, r.widget.screenInfo.H))
	r.widget.rect.FillColor = r.widget.bgColor
	canvas.Refresh(r.widget)
}

func (r *monitorWidgetRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.widget.cont}
}

func (r *monitorWidgetRenderer) Destroy() {}
