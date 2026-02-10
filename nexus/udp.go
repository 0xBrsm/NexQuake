package main

import (
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
)

const defaultNQServerIP = "127.13.37.9"

var nqServerIP = parseNQServerIP(os.Getenv("NQSERVER_IP"))

func parseNQServerIP(raw string) net.IP {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultNQServerIP
	}

	ip := net.ParseIP(raw).To4()
	if ip != nil {
		return ip
	}

	warnf("invalid NQSERVER_IP=%q; using %s", raw, defaultNQServerIP)
	return net.ParseIP(defaultNQServerIP).To4()
}

func relayListenAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4zero, Port: 0}
}

func relayListenAddrForClient(ip4 [4]byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4(),
		Port: 0,
	}
}

func serverSourcePortFromAddr(addr net.Addr) (srcPort int, ok bool) {
	udpAddr, isUDP := addr.(*net.UDPAddr)
	if !isUDP {
		return 0, false
	}
	if udpAddr.Port <= 0 || udpAddr.Port > 65535 {
		return 0, false
	}
	return udpAddr.Port, true
}

func serverUDPAddr(port int) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   nqServerIP,
		Port: port,
	}
}

func (r *Router) udpReadLoop() {
	debugRelay := os.Getenv("DEBUG_RELAY") == "1"

	buffer := make([]byte, 65536)
	for {
		if r.ctx.Err() != nil {
			return
		}

		n, remoteSrcAddr, err := r.udpConn.ReadFrom(buffer)
		if err != nil {
			// Linux may surface ICMP port-unreachable as ECONNREFUSED on UDP
			// sockets after writes (especially when clients probe addresses
			// with no server bound). Ignore and keep the relay alive.
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}
			if r.ctx.Err() == nil {
				warnf("UDP read error: %v", err)
			}
			r.Close()
			return
		}

		packet := buffer[:n]

		// Extract server route from source address.
		srcPort, ok := serverSourcePortFromAddr(remoteSrcAddr)
		if !ok {
			continue
		}

		if debugRelay {
			debugf("DEBUG_RELAY\tudp<-server\tsrc=%s\tport=%d\tlen=%d\tbytes=% x",
				remoteSrcAddr.String(), srcPort, len(packet), packet[:min(len(packet), 24)])
		}

		r.sendWS(buildWSFrame(srcPort, packet), true)
	}
}

func (r *Router) udpWrite(dstPort int, payload []byte) {
	if r.ctx.Err() != nil || len(payload) == 0 {
		return
	}

	debugRelay := os.Getenv("DEBUG_RELAY") == "1"

	dst := serverUDPAddr(dstPort)
	if debugRelay {
		debugf("DEBUG_RELAY\tudp->server\tdst=%s\tlen=%d\tbytes=% x",
			dst.String(), len(payload), payload[:min(len(payload), 24)])
	}

	_, err := r.udpConn.WriteToUDP(payload, dst)
	if err != nil && errors.Is(err, syscall.ECONNREFUSED) {
		// Linux may report ICMP port-unreachable from a previous send as
		// ECONNREFUSED on the next socket op. Retry once.
		_, err = r.udpConn.WriteToUDP(payload, dst)
	}
	if err != nil && r.ctx.Err() == nil {
		warnf("UDP write error: %v", err)
	}
}
