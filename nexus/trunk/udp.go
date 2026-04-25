package trunk

import (
	"errors"
	"net"
	"syscall"
)

// DefaultServerIP is the loopback address of the NQ dedicated server that
// the relay forwards UDP traffic to. Override by passing a different IP to
// [NewVirtualIPAllocator].
const DefaultServerIP = "127.0.0.1"

// listenAddrForClient returns a UDP listen address bound to the client's
// VirtualIP (any port).
func listenAddrForClient(ip4 [4]byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4(),
		Port: 0,
	}
}

// serverSourcePortFromAddr extracts the source port from a UDP address
// returned by ReadFrom. Returns ok=false for non-UDP addresses and port 0.
func serverSourcePortFromAddr(addr net.Addr) (srcPort int, ok bool) {
	udpAddr, isUDP := addr.(*net.UDPAddr)
	if !isUDP || !isValidServerPort(udpAddr.Port) {
		return 0, false
	}
	return udpAddr.Port, true
}

// serverUDPAddr returns the UDP address for a game server at the given port.
func serverUDPAddr(serverIP net.IP, port int) *net.UDPAddr {
	return &net.UDPAddr{IP: serverIP, Port: port}
}

// udpReadLoop reads datagrams from udpConn and forwards them as tunnel frames.
func (c *Conn) udpReadLoop() {
	buffer := make([]byte, 65536)
	for {
		if c.ctx.Err() != nil {
			return
		}

		n, remoteSrcAddr, err := c.udpConn.ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			if c.ctx.Err() == nil {
				c.warnf("UDP read error: %v", err)
			}
			c.Close()
			return
		}

		packet := buffer[:n]

		srcPort, ok := serverSourcePortFromAddr(remoteSrcAddr)
		if !ok {
			continue
		}
		if c.debugRelay {
			c.debugf("DEBUG_RELAY\tudp<-server\tsrc=%s\tport=%d\tlen=%d\tbytes=% x",
				remoteSrcAddr.String(), srcPort, len(packet), packet[:min(len(packet), 24)])
		}

		c.sendFrame(buildFrame(srcPort, packet), true)
	}
}

// udpWrite sends payload to a server port over UDP.
func (c *Conn) udpWrite(dstPort int, payload []byte) {
	if c.ctx.Err() != nil || len(payload) == 0 {
		return
	}

	dst := serverUDPAddr(c.alloc.serverIP, dstPort)
	if c.debugRelay {
		c.debugf("DEBUG_RELAY\tudp->server\tdst=%s\tlen=%d\tbytes=% x",
			dst.String(), len(payload), payload[:min(len(payload), 24)])
	}

	_, err := c.udpConn.WriteToUDP(payload, dst)
	if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
		_, err = c.udpConn.WriteToUDP(payload, dst)
	}
	if err != nil && c.ctx.Err() == nil {
		c.warnf("UDP write error: %v", err)
	}
}
