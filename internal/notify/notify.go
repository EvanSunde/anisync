// Package notify sends freedesktop notifications over D-Bus (no notify-send
// dependency) and dispatches action-button clicks.
package notify

import (
	"log"
	"sync"

	"github.com/godbus/dbus/v5"
)

const (
	iface = "org.freedesktop.Notifications"
	path  = "/org/freedesktop/Notifications"
)

type Notifier struct {
	conn     *dbus.Conn
	enabled  bool
	mu       sync.Mutex
	handlers map[uint32]func(action string)
}

func New(conn *dbus.Conn, enabled bool) *Notifier {
	n := &Notifier{conn: conn, enabled: enabled && conn != nil, handlers: map[uint32]func(string){}}
	if !n.enabled {
		return n
	}
	for _, m := range []string{"ActionInvoked", "NotificationClosed"} {
		_ = conn.AddMatchSignal(dbus.WithMatchInterface(iface), dbus.WithMatchMember(m), dbus.WithMatchObjectPath(path))
	}
	return n
}

// HandleSignal must be fed signals from the shared connection.
func (n *Notifier) HandleSignal(s *dbus.Signal) {
	if s.Path != path || len(s.Body) < 1 {
		return
	}
	id, _ := s.Body[0].(uint32)
	n.mu.Lock()
	h := n.handlers[id]
	switch s.Name {
	case iface + ".NotificationClosed":
		delete(n.handlers, id)
		h = nil
	case iface + ".ActionInvoked":
		delete(n.handlers, id)
	}
	n.mu.Unlock()
	if h != nil && s.Name == iface+".ActionInvoked" && len(s.Body) >= 2 {
		action, _ := s.Body[1].(string)
		go h(action)
	}
}

// Send shows a notification. actions are key,label pairs; onAction is called
// with the clicked key. Returns the notification id (0 on failure).
func (n *Notifier) Send(summary, body string, actions []string, onAction func(string)) uint32 {
	log.Printf("notify: %s — %s", summary, body)
	if !n.enabled {
		return 0
	}
	if actions == nil {
		actions = []string{}
	}
	hints := map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("anisync")}
	var id uint32
	err := n.conn.Object(iface, path).Call(iface+".Notify", 0,
		"anisync", uint32(0), "video-x-generic", summary, body, actions, hints, int32(-1)).Store(&id)
	if err != nil {
		log.Printf("notify: %v", err)
		return 0
	}
	if onAction != nil && len(actions) > 0 {
		n.mu.Lock()
		n.handlers[id] = onAction
		n.mu.Unlock()
	}
	return id
}

func (n *Notifier) Close(id uint32) {
	if !n.enabled || id == 0 {
		return
	}
	n.mu.Lock()
	delete(n.handlers, id)
	n.mu.Unlock()
	n.conn.Object(iface, path).Call(iface+".CloseNotification", 0, id)
}
