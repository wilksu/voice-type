package tray

import (
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
)

const (
	sniPath   = "/StatusNotifierItem"
	menuPath  = "/MenuBar"
	sniIface  = "org.kde.StatusNotifierItem"
	menuIface = "com.canonical.dbusmenu"
)

type Callbacks struct {
	OnToggleEnabled func(enabled bool)
	OnTogglePolish  func(enabled bool)
	OnOpenSettings  func()
	OnQuit          func()
}

type menuItem struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

type groupPropItem struct {
	ID    int32
	Props map[string]dbus.Variant
}

type Tray struct {
	conn *dbus.Conn
	cb   Callbacks

	mu            sync.Mutex
	enabled       bool
	polishEnabled bool
	hotkeyName    string
	revision      uint32
	props         *prop.Properties
}

func Start(enabled, polishEnabled bool, hotkeyName string, cb Callbacks) (*Tray, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, err
	}

	busName := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return nil, err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return nil, fmt.Errorf("could not acquire D-Bus name %s", busName)
	}

	t := &Tray{
		conn:          conn,
		cb:            cb,
		enabled:       enabled,
		polishEnabled: polishEnabled,
		hotkeyName:    hotkeyName,
		revision:      1,
	}

	sniObj := &sniHandler{tray: t}
	menuObj := &menuHandler{tray: t}

	if err := conn.Export(sniObj, sniPath, sniIface); err != nil {
		return nil, err
	}
	if err := conn.Export(menuObj, menuPath, menuIface); err != nil {
		return nil, err
	}

	propsSpec := map[string]map[string]*prop.Prop{
		sniIface: {
			"Category":   {Value: "ApplicationStatus", Writable: false, Emit: prop.EmitTrue},
			"Id":         {Value: "voice-type", Writable: false, Emit: prop.EmitTrue},
			"Title":      {Value: "voice-type", Writable: false, Emit: prop.EmitTrue},
			"Status":     {Value: "Active", Writable: false, Emit: prop.EmitTrue},
			"IconName":   {Value: "", Writable: false, Emit: prop.EmitTrue},
			"IconPixmap": {Value: renderTrayPixmap(t.enabled), Writable: true, Emit: prop.EmitTrue},
			"Menu":       {Value: dbus.ObjectPath(menuPath), Writable: false, Emit: prop.EmitTrue},
			"ItemIsMenu": {Value: true, Writable: false, Emit: prop.EmitTrue},
		},
		menuIface: {
			"Version":       {Value: uint32(3), Writable: false, Emit: prop.EmitFalse},
			"TextDirection": {Value: "ltr", Writable: false, Emit: prop.EmitFalse},
			"Status":        {Value: "normal", Writable: false, Emit: prop.EmitFalse},
			"IconThemePath": {Value: []string{}, Writable: false, Emit: prop.EmitFalse},
		},
	}
	t.props, _ = prop.Export(conn, sniPath, propsSpec)
	_, _ = prop.Export(conn, menuPath, map[string]map[string]*prop.Prop{menuIface: propsSpec[menuIface]})

	_ = conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: sniPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{Name: sniIface},
		},
	}), sniPath, "org.freedesktop.DBus.Introspectable")

	_ = conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: menuPath,
		Interfaces: []introspect.Interface{
			introspect.IntrospectData,
			prop.IntrospectData,
			{Name: menuIface},
		},
	}), menuPath, "org.freedesktop.DBus.Introspectable")

	watcher := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	if err := watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, busName).Err; err != nil {
		log.Printf("StatusNotifierWatcher registration skipped: %v", err)
	} else {
		log.Printf("System tray registered (%s)", busName)
	}

	return t, nil
}

func (t *Tray) iconName() string {
	if t.enabled {
		return "audio-input-microphone"
	}
	return "microphone-sensitivity-muted"
}

func (t *Tray) UpdateState(enabled, polishEnabled bool, hotkeyName string) {
	t.mu.Lock()
	t.enabled = enabled
	t.polishEnabled = polishEnabled
	if hotkeyName != "" {
		t.hotkeyName = hotkeyName
	}
	t.mu.Unlock()
	t.notifyUpdated()
}

func (t *Tray) notifyUpdated() {
	t.mu.Lock()
	t.revision++
	rev := t.revision
	enabled := t.enabled
	t.mu.Unlock()

	if t.props != nil {
		if err := t.props.Set(sniIface, "IconPixmap", dbus.MakeVariant(renderTrayPixmap(enabled))); err != nil {
			log.Printf("Tray Set IconPixmap skipped: %v", err)
			return
		}
	}
	_ = t.conn.Emit(sniPath, sniIface+".NewIcon")
	_ = t.conn.Emit(menuPath, menuIface+".LayoutUpdated", rev, int32(0))
}

func (t *Tray) buildItems() []menuItem {
	t.mu.Lock()
	enabled := t.enabled
	polish := t.polishEnabled
	hk := t.hotkeyName
	t.mu.Unlock()

	statusLabel := fmt.Sprintf("状态: 运行中 (%s)", hk)
	if !enabled {
		statusLabel = "状态: 已暂停"
	}

	enabledState := int32(0)
	if enabled {
		enabledState = 1
	}
	polishState := int32(0)
	if polish {
		polishState = 1
	}

	return []menuItem{
		{
			ID: 1,
			Props: map[string]dbus.Variant{
				"label":   dbus.MakeVariant(statusLabel),
				"enabled": dbus.MakeVariant(false),
				"visible": dbus.MakeVariant(true),
			},
		},
		{
			ID: 2,
			Props: map[string]dbus.Variant{
				"label":        dbus.MakeVariant("启用语音输入"),
				"enabled":      dbus.MakeVariant(true),
				"visible":      dbus.MakeVariant(true),
				"toggle-type":  dbus.MakeVariant("checkmark"),
				"toggle-state": dbus.MakeVariant(enabledState),
			},
		},
		{
			ID: 3,
			Props: map[string]dbus.Variant{
				"label":        dbus.MakeVariant("AI 长句润色 (LM Studio)"),
				"enabled":      dbus.MakeVariant(true),
				"visible":      dbus.MakeVariant(true),
				"toggle-type":  dbus.MakeVariant("checkmark"),
				"toggle-state": dbus.MakeVariant(polishState),
			},
		},
		{
			ID: 4,
			Props: map[string]dbus.Variant{
				"type":    dbus.MakeVariant("separator"),
				"visible": dbus.MakeVariant(true),
			},
		},
		{
			ID: 5,
			Props: map[string]dbus.Variant{
				"label":     dbus.MakeVariant("打开设置..."),
				"icon-name": dbus.MakeVariant("configure"),
				"enabled":   dbus.MakeVariant(true),
				"visible":   dbus.MakeVariant(true),
			},
		},
		{
			ID: 6,
			Props: map[string]dbus.Variant{
				"label":     dbus.MakeVariant("退出 voice-type"),
				"icon-name": dbus.MakeVariant("application-exit"),
				"enabled":   dbus.MakeVariant(true),
				"visible":   dbus.MakeVariant(true),
			},
		},
	}
}

func (t *Tray) Close() {
	// SessionBus() returns a process-wide shared connection; do not close it here.
}

type sniHandler struct {
	tray *Tray
}

func (h *sniHandler) Activate(x, y int32) *dbus.Error {
	h.tray.mu.Lock()
	h.tray.enabled = !h.tray.enabled
	enabled := h.tray.enabled
	h.tray.mu.Unlock()

	h.tray.notifyUpdated()
	if h.tray.cb.OnToggleEnabled != nil {
		h.tray.cb.OnToggleEnabled(enabled)
	}
	return nil
}

func (h *sniHandler) SecondaryActivate(x, y int32) *dbus.Error {
	return h.Activate(x, y)
}

func (h *sniHandler) ContextMenu(x, y int32) *dbus.Error {
	return nil
}

func (h *sniHandler) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

type menuHandler struct {
	tray *Tray
}

func (m *menuHandler) GetLayout(parentID int32, recursionDepth int32, propertyNames []string) (uint32, menuItem, *dbus.Error) {
	m.tray.mu.Lock()
	rev := m.tray.revision
	m.tray.mu.Unlock()

	items := m.tray.buildItems()
	if parentID != 0 {
		for _, it := range items {
			if it.ID == parentID {
				return rev, it, nil
			}
		}
		return rev, menuItem{ID: parentID, Props: map[string]dbus.Variant{}}, nil
	}

	children := make([]dbus.Variant, 0, len(items))
	for _, it := range items {
		children = append(children, dbus.MakeVariant(it))
	}

	root := menuItem{
		ID: 0,
		Props: map[string]dbus.Variant{
			"children-display": dbus.MakeVariant("submenu"),
		},
		Children: children,
	}
	return rev, root, nil
}

func (m *menuHandler) GetGroupProperties(ids []int32, propertyNames []string) ([]groupPropItem, *dbus.Error) {
	items := m.tray.buildItems()
	want := make(map[int32]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []groupPropItem
	for _, it := range items {
		if len(ids) == 0 || want[it.ID] {
			out = append(out, groupPropItem{ID: it.ID, Props: it.Props})
		}
	}
	return out, nil
}

func (m *menuHandler) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	for _, it := range m.tray.buildItems() {
		if it.ID == id {
			if v, ok := it.Props[name]; ok {
				return v, nil
			}
		}
	}
	return dbus.MakeVariant(""), nil
}

func (m *menuHandler) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	switch id {
	case 2:
		m.tray.mu.Lock()
		m.tray.enabled = !m.tray.enabled
		enabled := m.tray.enabled
		m.tray.mu.Unlock()
		m.tray.notifyUpdated()
		if m.tray.cb.OnToggleEnabled != nil {
			m.tray.cb.OnToggleEnabled(enabled)
		}
	case 3:
		m.tray.mu.Lock()
		m.tray.polishEnabled = !m.tray.polishEnabled
		polish := m.tray.polishEnabled
		m.tray.mu.Unlock()
		m.tray.notifyUpdated()
		if m.tray.cb.OnTogglePolish != nil {
			m.tray.cb.OnTogglePolish(polish)
		}
	case 5:
		if m.tray.cb.OnOpenSettings != nil {
			go m.tray.cb.OnOpenSettings()
		}
	case 6:
		if m.tray.cb.OnQuit != nil {
			m.tray.cb.OnQuit()
		}
	}
	return nil
}

func (m *menuHandler) EventGroup(events []struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}) ([]int32, *dbus.Error) {
	for _, ev := range events {
		_ = m.Event(ev.ID, ev.EventID, ev.Data, ev.Timestamp)
	}
	return nil, nil
}

func (m *menuHandler) AboutToShow(id int32) (bool, *dbus.Error) {
	return true, nil
}

func (m *menuHandler) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	return ids, nil, nil
}
