package nqrelay

import (
	"errors"
	"net"
	"syscall"
)

// DefaultNQServerIP is the loopback address of the NQ dedicated server that
// the relay forwards UDP traffic to. Override by passing a different IP to
// [NewNQIPAllocator].
const DefaultNQServerIP = "127.0.0.1"

// listenAddrForClient returns a UDP listen address bound to the client's
// NQIP (any port).
func listenAddrForClient(ip4 [4]byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4(),
		Port: 0,
	}
}

// ServerSourcePortFromAddr extracts the source port from a UDP address
// returned by ReadFrom. Returns ok=false for non-UDP addresses and port 0.
func ServerSourcePortFromAddr(addr net.Addr) (srcPort int, ok bool) {
	udpAddr, isUDP := addr.(*net.UDPAddr)
	if !isUDP || !isValidServerPort(udpAddr.Port) {
		return 0, false
	}
	return udpAddr.Port, true
}

// ServerUDPAddr returns the UDP address for a game server at the given port.
func ServerUDPAddr(serverIP net.IP, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: serverIP, Port: port}
}

// udpReadLoop reads datagrams from udpConn and forwards them as WS frames.
func (r *Relay) udpReadLoop() {
	buffer := make([]byte, 65536)
	for {
		if r.ctx.Err() != nil {
			return
		}

		n, remoteSrcAddr, err := r.udpConn.ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			if r.ctx.Err() == nil {
				r.warnf("UDP read error: %v", err)
			}
			r.Close()
			return
		}

		packet := buffer[:n]

		srcPort, ok := ServerSourcePortFromAddr(remoteSrcAddr)
		if !ok {
			continue
		}
		if r.debugRelay {
			r.debugf("DEBUG_RELAY\tudp<-server\tsrc=%s\tport=%d\tlen=%d\tbytes=% x",
				remoteSrcAddr.String(), srcPort, len(packet), packet[:min(len(packet), 24)])
		}

		r.sendWS(buildWSFrame(srcPort, packet), true)
	}
}

// udpWrite sends payload to a server port over UDP.
func (r *Relay) udpWrite(dstPort int, payload []byte) {
	if r.ctx.Err() != nil || len(payload) == 0 {
		return
	}

	dst := ServerUDPAddr(r.alloc.serverIP, dstPort)
	if r.debugRelay {
		r.debugf("DEBUG_RELAY\tudp->server\tdst=%s\tlen=%d\tbytes=% x",
			dst.String(), len(payload), payload[:min(len(payload), 24)])
	}

	_, err := r.udpConn.WriteToUDP(payload, dst)
	if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
		_, err = r.udpConn.WriteToUDP(payload, dst)
	}
	if err != nil && r.ctx.Err() == nil {
		r.warnf("UDP write error: %v", err)
	}
}
