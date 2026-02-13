package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

type Router struct {
	ws        *websocket.Conn
	udpConn   *net.UDPConn
	wsTx      chan []byte
	clientIP  [4]byte
	sourceKey string
	isAdmin   bool

	routeMu          sync.RWMutex
	activeServerPort int

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func NewRouter(ws *websocket.Conn, sourceKey string, isAdmin bool) (*Router, error) {
	ctx, cancel := context.WithCancel(context.Background())

	clientIP, err := allocateRelaySourceIPv4(sourceKey)
	if err != nil {
		cancel()
		return nil, err
	}

	udpConn, err := net.ListenUDP("udp4", relayListenAddrForClient(clientIP))
	if err != nil {
		releaseRelaySourceIPv4(clientIP)
		cancel()
		return nil, fmt.Errorf("listen udp: %w", err)
	}

	router := &Router{
		ws:        ws,
		udpConn:   udpConn,
		wsTx:      make(chan []byte, 1024),
		clientIP:  clientIP,
		sourceKey: strings.TrimSpace(sourceKey),
		isAdmin:   isAdmin,
		ctx:       ctx,
		cancel:    cancel,
	}
	globalClientSessions.Track(router)
	return router, nil
}

func (r *Router) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		globalClientSessions.Untrack(r)
		r.cancel()
		if r.udpConn != nil {
			_ = r.udpConn.Close()
		}
		if r.ws != nil {
			_ = r.ws.Close()
		}
		releaseRelaySourceIPv4(r.clientIP)
		r.clientIP = [4]byte{}
	})
}

func (r *Router) VirtualClientIP() string {
	if r == nil || r.clientIP[0] == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", r.clientIP[0], r.clientIP[1], r.clientIP[2], r.clientIP[3])
}

func (r *Router) SourceKey() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.sourceKey)
}

func (r *Router) noteServerRoutePort(port int) {
	if r == nil || port < 1 || port > 65535 {
		return
	}
	r.routeMu.Lock()
	r.activeServerPort = port
	r.routeMu.Unlock()
}

func (r *Router) ActiveServerPort() int {
	if r == nil {
		return 0
	}
	r.routeMu.RLock()
	port := r.activeServerPort
	r.routeMu.RUnlock()
	return port
}

func (r *Router) Run() {
	if r == nil {
		return
	}

	if frame := buildWSClientIdentityFrame(r.clientIP); len(frame) > 0 {
		r.sendWS(frame, true)
	}

	go r.udpReadLoop()
	go r.wsWriteLoop()
	r.wsReadLoop()
	r.Close()
}

func (r *Router) sendWS(frame []byte, drop bool) {
	if r == nil || len(frame) == 0 || r.ctx.Err() != nil {
		return
	}

	if drop {
		select {
		case <-r.ctx.Done():
		case r.wsTx <- frame:
		default:
			warnf("ws tx channel full, dropping packet")
		}
		return
	}

	select {
	case <-r.ctx.Done():
	case r.wsTx <- frame:
	}
}

func buildWSFrame(port int, payload []byte) []byte {
	if port < 0 || port > 65535 {
		return nil
	}
	frame := make([]byte, wsPortHeaderSize+len(payload))
	frame[0] = byte((port >> 8) & 0xff)
	frame[1] = byte(port & 0xff)
	copy(frame[wsPortHeaderSize:], payload)
	return frame
}

func (r *Router) handleWSFrame(packet []byte) {
	if len(packet) < wsPortHeaderSize {
		return
	}

	dstPort := int(packet[0])<<8 | int(packet[1])
	payload := packet[wsPortHeaderSize:]

	if dstPort == 0 {
		if globalServerManager != nil && isCCREQServerInfo(payload) {
			entries := snapshotForSlist(globalServerManager)
			listPayload, _ := buildCCREPServerList(entries)
			r.sendWS(buildWSFrame(0, listPayload), false)
		} else {
			r.handleAdminFrame(payload)
		}
		return
	}

	r.noteServerRoutePort(dstPort)
	r.udpWrite(dstPort, payload)
}

const (
	wsPortHeaderSize = 2
)

const (
	netFlagLengthMask uint32 = 0x0000ffff
	netFlagCtl        uint32 = 0x80000000

	netProtocolVersion byte = 3

	ccreqServerInfo byte = 0x02
	ccrepServerInfo byte = 0x83
)

type serverListEntry struct {
	ListenPort int
	Hostname   string
	MapName    string
	GameDir    string
	Players    byte
	MaxPlayers byte
}

// Quake constants (see quakedef.h/net.h):
// MAX_DATAGRAM=1024 and NET_HEADERSIZE=8 => NET_DATAGRAMSIZE=1032.
// NexQuake's WS transport enforces this limit for a single tunneled "UDP datagram".
const maxNetDatagramSize = 1024 + 8

func buildCCREQServerInfo() []byte {
	// Matches WinQuake net_dgrm.c:
	//   long header (NETFLAG_CTL | length)
	//   byte CCREQ_SERVER_INFO
	//   string "QUAKE"
	//   byte NET_PROTOCOL_VERSION
	buf := make([]byte, 0, 64)
	buf = append(buf, 0, 0, 0, 0) // placeholder for the header
	buf = append(buf, ccreqServerInfo)
	buf = appendCString(buf, "QUAKE")
	buf = append(buf, netProtocolVersion)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf
}

func truncateQuakeString(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if s == "" {
		return s
	}
	// Quake UI fields are short and treated as byte strings; clamp by bytes.
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}

func buildCCREPServerList(entries []serverListEntry) ([]byte, int) {
	// Format:
	//   u32 control header (NETFLAG_CTL | length)
	//   u8  CCREP_SERVER_INFO (0x83) - overloaded for nexus aggregated list mode
	//   u8  count
	//   repeated entries:
	//     cstring server_port (e.g. "26000")
	//     cstring host_name (<= 15 chars recommended)
	//     cstring level_name (<= 15 chars recommended)
	//     cstring game_dir (<= 15 chars recommended)
	//     u8 players, u8 maxPlayers, u8 protocol_version
	buf := make([]byte, 0, 512)
	buf = append(buf, 0, 0, 0, 0) // placeholder header
	buf = append(buf, ccrepServerInfo)
	countIndex := len(buf)
	buf = append(buf, 0) // count placeholder

	count := 0
	for _, e := range entries {
		serverPort := e.ListenPort
		if serverPort <= 0 || serverPort > 65535 {
			continue
		}
		hostname := e.Hostname
		mapName := e.MapName
		gameDir := e.GameDir
		if hostname == "" {
			hostname = "UNNAMED"
		}
		if mapName == "" {
			mapName = "?"
		}
		if gameDir == "" {
			gameDir = "id1"
		}

		serverPortText := strconv.Itoa(serverPort)

		hostname = truncateQuakeString(hostname, 15)
		mapName = truncateQuakeString(mapName, 15)
		gameDir = truncateQuakeString(gameDir, 15)

		// Compute the entry size before appending to stay within Quake's datagram limit.
		entrySize := len(serverPortText) + 1 + len(hostname) + 1 + len(mapName) + 1 + len(gameDir) + 1 + 3
		if len(buf)+entrySize > maxNetDatagramSize {
			break
		}

		buf = appendCString(buf, serverPortText)
		buf = appendCString(buf, hostname)
		buf = appendCString(buf, mapName)
		buf = appendCString(buf, gameDir)
		buf = append(buf, e.Players, e.MaxPlayers, netProtocolVersion)
		count++
	}

	buf[countIndex] = byte(count)

	control := netFlagCtl | uint32(len(buf))
	binary.BigEndian.PutUint32(buf[0:4], control)
	return buf, count
}

func isCCREQServerInfo(payload []byte) bool {
	_, ok := parseCCREQServerInfo(payload)
	return ok
}

func parseCCREQServerInfo(payload []byte) (protocol byte, ok bool) {
	if len(payload) < 4+1 {
		return 0, false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control &^ netFlagLengthMask) != netFlagCtl {
		return 0, false
	}
	if int(control&netFlagLengthMask) != len(payload) {
		return 0, false
	}
	if payload[4] != ccreqServerInfo {
		return 0, false
	}
	i := 5
	game, next, ok := readCString(payload, i)
	if !ok || game != "QUAKE" {
		return 0, false
	}
	i = next
	if i >= len(payload) {
		return 0, false
	}
	protocol = payload[i]
	return protocol, protocol == netProtocolVersion
}

func parseCCREPServerInfo(payload []byte) (hostname, mapName string, players, maxPlayers, protocol byte, ok bool) {
	if len(payload) < 4+1 {
		return "", "", 0, 0, 0, false
	}
	control := binary.BigEndian.Uint32(payload[0:4])
	if (control &^ netFlagLengthMask) != netFlagCtl {
		return "", "", 0, 0, 0, false
	}
	if int(control&netFlagLengthMask) != len(payload) {
		return "", "", 0, 0, 0, false
	}
	if payload[4] != ccrepServerInfo {
		return "", "", 0, 0, 0, false
	}
	i := 5
	// server_address (ignored)
	_, next, ok := readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next

	hostname, next, ok = readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next
	mapName, next, ok = readCString(payload, i)
	if !ok {
		return "", "", 0, 0, 0, false
	}
	i = next

	if i+3 > len(payload) {
		return "", "", 0, 0, 0, false
	}
	players = payload[i]
	maxPlayers = payload[i+1]
	protocol = payload[i+2]
	return hostname, mapName, players, maxPlayers, protocol, protocol == netProtocolVersion
}

func appendCString(buf []byte, s string) []byte {
	buf = append(buf, []byte(s)...)
	buf = append(buf, 0)
	return buf
}

func readCString(buf []byte, start int) (string, int, bool) {
	if start < 0 || start >= len(buf) {
		return "", 0, false
	}
	rest := buf[start:]
	n := bytes.IndexByte(rest, 0)
	if n < 0 {
		return "", 0, false
	}
	return string(rest[:n]), start + n + 1, true
}
