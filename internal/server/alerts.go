package server

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Observability (PLAN.md §7.5): the backend answers "how do I know an agent
// stopped?" two ways — it pushes an alert when a device that was reporting goes
// silent, and it pings an external dead-man's switch so its own death is noticed.

// Notifier delivers a short alert. Abstracted so the offline-detection logic is
// testable without real HTTP.
type Notifier interface {
	Send(title, message, priority string)
}

// ntfyNotifier posts to an ntfy-compatible endpoint (ntfy.sh or self-hosted):
// the request body is the message, with Title/Priority headers. Pushover,
// Telegram, etc. can be reached by pointing this at an ntfy-compatible relay.
type ntfyNotifier struct {
	url    string
	client *http.Client
}

func newNtfyNotifier(url string) *ntfyNotifier {
	return &ntfyNotifier{url: url, client: &http.Client{Timeout: 10 * time.Second}}
}

func (n *ntfyNotifier) Send(title, message, priority string) {
	req, err := http.NewRequest(http.MethodPost, n.url, bytes.NewReader([]byte(message)))
	if err != nil {
		log.Printf("alert: %v", err)
		return
	}
	req.Header.Set("Title", title)
	if priority != "" {
		req.Header.Set("Priority", priority)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		log.Printf("alert send: %v", err)
		return
	}
	resp.Body.Close()
}

// checkOffline detects devices that crossed the online/offline threshold since
// the last check and alerts on each transition (once, not every tick). Active
// devices only — pending/expired/revoked never alert. The first pass after
// startup only seeds state (no alerts), so a restart doesn't spam alerts for
// devices that were already off.
func (s *Server) checkOffline(now int64) {
	if s.notifier == nil {
		return
	}
	devices, err := s.store.DeviceStatuses(now)
	if err != nil {
		log.Printf("offline check: %v", err)
		return
	}
	threshold := int64(s.offlineAfter.Seconds())

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range devices {
		if d.Status != "active" || d.LastSeen == 0 {
			continue
		}
		offline := now-d.LastSeen > threshold
		alerted := s.alerted[d.DeviceUUID]

		if !s.alertSeeded {
			if offline {
				s.alerted[d.DeviceUUID] = true // seed silently on first pass
			}
			continue
		}
		switch {
		case offline && !alerted:
			s.alerted[d.DeviceUUID] = true
			name := d.Name
			if name == "" {
				name = d.Hostname
			}
			mins := (now - d.LastSeen) / 60
			s.notifier.Send("Device offline",
				fmt.Sprintf("%s stopped reporting (last seen %d min ago).", name, mins), "high")
		case !offline && alerted:
			delete(s.alerted, d.DeviceUUID)
			name := d.Name
			if name == "" {
				name = d.Hostname
			}
			s.notifier.Send("Device back online", fmt.Sprintf("%s is reporting again.", name), "default")
		}
	}
	s.alertSeeded = true
}

// StartAlertLoop runs the offline-device check on interval until ctx is done.
func (s *Server) StartAlertLoop(ctx context.Context, interval time.Duration) {
	if s.notifier == nil {
		return
	}
	go func() {
		s.checkOffline(s.now().Unix()) // seed immediately
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.checkOffline(s.now().Unix())
			}
		}
	}()
}

// StartDeadManLoop pings an external monitor (e.g. healthchecks.io) every
// interval. If the backend dies, the pings stop and the external service raises
// the alarm — so a dead server, which can't alert on its own, is still noticed
// (PLAN.md §7.5). No-op if url is empty.
func (s *Server) StartDeadManLoop(ctx context.Context, url string, interval time.Duration) {
	if url == "" {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	ping := func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	go func() {
		ping()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ping()
			}
		}
	}()
}
