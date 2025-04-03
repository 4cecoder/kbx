package ui

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
	// Update the stored offset *before* moving
	newOffset := m.parentOffset.Add(delta)

	// Check bounds of the parent container? (Requires passing parent size or canvas)
	// For now, allow dragging anywhere

	m.parentOffset = newOffset // Update the offset within the parent

	// Move the widget itself. The layout container (NewWithoutLayout) won't
	// automatically use parentOffset, so we need to trigger the move.
	m.Move(m.Position().Add(delta))

	m.lastDragPos = e.Position
	// No need to Refresh the widget itself, Move does that.

}

// DragEnd is called when dragging stops
func (m *monitorWidget) DragEnd() {
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
func (m *monitorWidget) MouseDown(e *desktop.MouseEvent) {
	m.dragStart = e.Position
	m.lastDragPos = e.Position
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
	m.Move(pos) // Move the widget to the initial offset
}

// --- Renderer ---

type monitorWidgetRenderer struct {
	widget *monitorWidget
}

func (r *monitorWidgetRenderer) Layout(size fyne.Size) {
	r.widget.cont.Resize(size)
}

func (r *monitorWidgetRenderer) MinSize() fyne.Size {
	minLabel := r.widget.label.MinSize()
	return fyne.NewSize(minLabel.Width+20, minLabel.Height+20)
}

func (r *monitorWidgetRenderer) Refresh() {
	r.widget.label.SetText(fmt.Sprintf("%s\nID: %d\n%dx%d", r.widget.hostname, r.widget.screenInfo.ID, r.widget.screenInfo.W, r.widget.screenInfo.H))
	r.widget.rect.FillColor = r.widget.bgColor
	canvas.Refresh(r.widget.rect)
	r.widget.label.Refresh()
	// canvas.Refresh(r.widget) // BaseWidget refresh handles this
}

func (r *monitorWidgetRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.widget.cont}
}

func (r *monitorWidgetRenderer) Destroy() {}
