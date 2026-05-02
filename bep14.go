// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// Portions derived from https://gitlab.com/axet/libtorrent/-/blob/master/lpd.go
// by Alexey Kuznetsov <axet@me.com>, relicensed under MPL-2.0 with the
// author's permission.

package torrent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Local Service Discovery per BEP-14.
// http://bittorrent.org/beps/bep_0014.html

const (
	bep14Host4    = "239.192.152.143:6771"
	bep14Host6    = "[ff15::efc0:988f]:6771"
	bep14Announce = "BT-SEARCH * HTTP/1.1\r\n" +
		"Host: %s\r\n" +
		"Port: %s\r\n" +
		"%s" +
		"\r\n" +
		"\r\n"
	bep14AnnounceInfohash = "Infohash: %s\r\n"
	bep14LongTimeout      = 10 * time.Second
	bep14ShortTimeout     = 1 * time.Second
	bep14MaxPacketSize    = 1400
)

type lpdClient interface {
	LocalPort() (port int)
	OnLPDAnnouncement(addr string, infohashes []string)
	TorrentInfohashesAndPort() (port int, infohashes []string)
}

type lpdConn struct {
	ctx    context.Context
	cancel context.CancelFunc
	force  chan struct{}

	lpd          *lpdServer
	network      string
	addr         *net.UDPAddr
	mcListener   *net.UDPConn
	mcPublisher  *net.UDPConn
	mcPacketConn mcPacketConn
	host         string
	logger       log.Logger
}

type mcPacketConn interface {
	SetMulticastInterface(*net.Interface) error
}

func newMcPacketConn(network string, c net.PacketConn) (mcPacketConn, error) {
	switch network {
	case "udp4":
		return ipv4.NewPacketConn(c), nil
	case "udp6":
		return ipv6.NewPacketConn(c), nil
	default:
		return nil, fmt.Errorf("unsupported network %q", network)
	}
}

func sourceUdpAddress(iface *net.Interface, network string) (*net.UDPAddr, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}

		ip := ipNet.IP

		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}

		switch network {
		case "udp4":
			if ip.To4() != nil {
				return &net.UDPAddr{IP: ip}, nil
			}
		case "udp6":
			if ip.To4() == nil && ip.To16() != nil {
				return &net.UDPAddr{IP: ip}, nil
			}
		default:
			return nil, errors.New("invalid network type")
		}
	}

	return nil, errors.New("no suitable IP found")
}

func lpdConnNew(network string, host string, lpd *lpdServer, config LocalServiceDiscoveryConfig) *lpdConn {
	ctx, cancel := context.WithCancel(context.Background())
	m := &lpdConn{
		ctx:     ctx,
		cancel:  cancel,
		force:   make(chan struct{}, 1),
		lpd:     lpd,
		network: network,
		host:    host,
		logger:  log.Default,
	}

	var err error

	m.addr, err = net.ResolveUDPAddr(m.network, m.host)
	if err != nil {
		cancel()
		return nil
	}

	m.mcListener, err = net.ListenMulticastUDP(m.network, nil, m.addr)
	if err != nil {
		cancel()
		return nil
	}

	if config.Ifi != "" {
		iface, err := net.InterfaceByName(config.Ifi)
		if err != nil {
			cancel()
			return nil
		}

		srcAddr, err := sourceUdpAddress(iface, network)
		if err != nil {
			cancel()
			return nil
		}

		m.mcPublisher, err = net.DialUDP(network, srcAddr, m.addr)
		if err != nil {
			cancel()
			return nil
		}

		m.mcPacketConn, err = newMcPacketConn(network, m.mcPublisher)
		if err != nil {
			cancel()
			return nil
		}

		if err := m.mcPacketConn.SetMulticastInterface(iface); err != nil {
			cancel()
			return nil
		}
	} else {
		m.mcPublisher, err = net.DialUDP(network, nil, m.addr)
		if err != nil {
			cancel()
			return nil
		}
	}

	return m
}

func (m *lpdConn) receiver(client lpdClient) {
	for {
		buf := make([]byte, 2000)
		_, from, err := m.mcListener.ReadFromUDP(buf)
		if err != nil {
			if m.ctx.Err() != nil {
				return
			}
			continue
		}
		m.handleAnnouncePacket(client, buf, from)
	}
}

func (m *lpdConn) handleAnnouncePacket(client lpdClient, buf []byte, from *net.UDPAddr) {
	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(buf)))
	if err != nil {
		return
	}

	if req.Method != "BT-SEARCH" {
		return
	}

	ihs := req.Header[http.CanonicalHeaderKey("Infohash")]
	if ihs == nil {
		return
	}

	port := req.Header.Get("Port")
	if port == "" {
		return
	}

	addr, err := net.ResolveUDPAddr(m.network, net.JoinHostPort(from.IP.String(), port))
	if err != nil {
		return
	}

	// Ignore own messages
	if m.mcPublisher != nil {
		publisherAddr := m.mcPublisher.LocalAddr().(*net.UDPAddr)
		if client.LocalPort() == addr.Port && from.IP.Equal(publisherAddr.IP) {
			return
		}
	}

	m.lpd.mu.Lock()
	m.lpd.peer(addr.String())
	m.lpd.refresh()
	m.lpd.mu.Unlock()

	// Convert BEP14 peer into internal Peer format (NO Addr field)
	peer := Peer{
		IP:     addr.IP,
		Port:   addr.Port,
		Source: PeerSourceLPD,
	}

	client.OnLPDAnnouncement(addr.String(), ihs)
	client.(*Client).torrent.AddPeers([]Peer{peer})
}

func lpdPeer(t *Torrent, p string) {
	host, port, err := net.SplitHostPort(p)
	if err != nil {
		return
	}
	pi, err := strconv.Atoi(port)
	if err != nil {
		return
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return
	}

	peer := Peer{
		IP:     ip,
		Port:   pi,
		Source: PeerSourceLPD,
	}

	t.logger.Println("lpdPeer", "Adding peer", net.JoinHostPort(ip.String(), port))
	t.AddPeers([]Peer{peer})
}
