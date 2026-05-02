package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	taskCompleteRE = regexp.MustCompile(`^completed task (task-[a-f0-9]+) \(from ([^)]+)\):\s*(.*)$`)
	taskFailedRE   = regexp.MustCompile(`^failed task (task-[a-f0-9]+) \(from ([^)]+)\):\s*(.*)$`)
)

func writeWSFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | (opcode & 0x0f)}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n < 65536:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func (c *wsClient) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return writeWSFrame(c.conn, opcode, payload)
}

func (g *gateway) removeWSConn(target *wsClient) {
	g.wsMu.Lock()
	defer g.wsMu.Unlock()
	out := g.wsConns[:0]
	for _, c := range g.wsConns {
		if c != target {
			out = append(out, c)
		}
	}
	g.wsConns = out
}

func (g *gateway) broadcastEvent(e Event) {
	g.broadcastPayload(e)
}

func (g *gateway) broadcastPayload(v interface{}) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}
	g.wsMu.Lock()
	conns := append([]*wsClient(nil), g.wsConns...)
	g.wsMu.Unlock()
	for _, c := range conns {
		if err := c.writeFrame(0x1, payload); err != nil {
			_ = c.conn.Close()
			g.removeWSConn(c)
		}
	}
}

func parseTaskSocketEvent(e Event) (taskSocketEvent, bool) {
	if e.Type != "task" {
		return taskSocketEvent{}, false
	}
	if matches := taskCompleteRE.FindStringSubmatch(e.Summary); len(matches) == 4 {
		return taskSocketEvent{
			Type:        "task_complete",
			Agent:       e.Agent,
			DelegatedBy: strings.TrimSpace(matches[2]),
			TaskID:      matches[1],
			Result:      strings.TrimSpace(matches[3]),
			Timestamp:   e.Time,
		}, true
	}
	if matches := taskFailedRE.FindStringSubmatch(e.Summary); len(matches) == 4 {
		return taskSocketEvent{
			Type:        "task_failed",
			Agent:       e.Agent,
			DelegatedBy: strings.TrimSpace(matches[2]),
			TaskID:      matches[1],
			Error:       strings.TrimSpace(matches[3]),
			Timestamp:   e.Time,
		}, true
	}
	return taskSocketEvent{}, false
}

func (g *gateway) notifyDelegatingAgent(evt taskSocketEvent) {
	if g.settings != nil && !g.settings.GetBool("callbacks.enabled", true) {
		return
	}
	delegatedBy := strings.TrimSpace(evt.DelegatedBy)
	if delegatedBy == "" {
		return
	}
	agent, ok := g.getAgent(delegatedBy)
	if !ok {
		log.Printf("task callback: unknown delegating agent %q for task %s", delegatedBy, evt.TaskID)
		return
	}
	content := ""
	switch evt.Type {
	case "task_complete":
		content = fmt.Sprintf("[Task Complete] %s finished task %s: %s", evt.Agent, evt.TaskID, evt.Result)
	case "task_failed":
		content = fmt.Sprintf("[Task Failed] %s failed task %s: %s", evt.Agent, evt.TaskID, evt.Error)
	default:
		return
	}
	sessionID := "task-callbacks"
	if g.settings == nil || g.settings.GetString("callbacks.session_routing", "active") == "active" {
		g.lastSessionMu.RLock()
		if last := strings.TrimSpace(g.lastSession[delegatedBy]); last != "" {
			sessionID = last
		}
		g.lastSessionMu.RUnlock()
	}
	body, err := json.Marshal(map[string]string{
		"content":    content,
		"session_id": sessionID,
	})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: agentRequestTimeout}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(agent.URL, "/")+"/message", bytes.NewReader(body))
	if err != nil {
		log.Printf("task callback: build request failed for %s: %v", delegatedBy, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("task callback: post to %s failed: %v", delegatedBy, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		log.Printf("task callback: post to %s returned %d: %s", delegatedBy, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

func readWSFrame(r io.Reader) (opcode byte, payload []byte, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode = hdr[0] & 0x0f
	masked := (hdr[1] & 0x80) != 0
	payloadLen := int64(hdr[1] & 0x7f)
	switch payloadLen {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		payloadLen = int64(ext[0])<<56 | int64(ext[1])<<48 | int64(ext[2])<<40 | int64(ext[3])<<32 |
			int64(ext[4])<<24 | int64(ext[5])<<16 | int64(ext[6])<<8 | int64(ext[7])
	}
	if payloadLen < 0 || payloadLen > 1<<20 {
		return 0, nil, fmt.Errorf("websocket payload too large: %d", payloadLen)
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(r, maskKey[:]); err != nil {
			return 0, nil, err
		}
	}
	payload = make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, payload, nil
}

func wsAccept(key string) string {
	h := sha1.New()
	_, _ = h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (g *gateway) eventsNewest() []Event {
	g.eventMu.Lock()
	defer g.eventMu.Unlock()
	out := make([]Event, len(g.events))
	for i := range g.events {
		out[i] = g.events[len(g.events)-1-i]
	}
	return out
}

func (g *gateway) registerWebsocketRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws/events", func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		log.Printf("ws: key=%q accept=%q", key, wsAccept(key))
		if key == "" {
			http.Error(w, "missing websocket key", http.StatusBadRequest)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "websocket unsupported", http.StatusInternalServerError)
			return
		}
		conn, rw, err := hj.Hijack()
		if err != nil {
			return
		}
		resp := "HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
		if _, err := rw.WriteString(resp); err != nil {
			_ = conn.Close()
			return
		}
		if err := rw.Flush(); err != nil {
			_ = conn.Close()
			return
		}
		log.Printf("ws: client connected from %s", conn.RemoteAddr())
		client := &wsClient{conn: conn, r: rw.Reader}
		g.wsMu.Lock()
		g.wsConns = append(g.wsConns, client)
		g.wsMu.Unlock()
		defer func() {
			log.Printf("ws: client disconnected: %s", client.conn.RemoteAddr())
			_ = client.conn.Close()
			g.removeWSConn(client)
		}()
		const (
			pingInterval = 30 * time.Second
			pongWait     = 60 * time.Second
		)
		_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
		log.Printf("ws: entering read loop")
		stopPing := make(chan struct{})
		go func() {
			t := time.NewTicker(pingInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					if err := client.writeFrame(0x9, nil); err != nil {
						log.Printf("ws: ping write error: %v", err)
						_ = client.conn.Close()
						return
					}
				case <-stopPing:
					return
				}
			}
		}()
		defer close(stopPing)
		for {
			opcode, payload, err := readWSFrame(client.r)
			if err != nil {
				log.Printf("ws: read error: %v", err)
				return
			}
			switch opcode {
			case 0x8:
				log.Printf("ws: client sent close frame")
				return
			case 0x9:
				if err := client.writeFrame(0xA, payload); err != nil {
					return
				}
			case 0xA:
				_ = client.conn.SetReadDeadline(time.Now().Add(pongWait))
			default:
			}
		}
	})
}
