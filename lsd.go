package torrent

import (
	"io"
	"net"
	"net/netip"

	"github.com/anacrolix/log"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/trim21/bep14"
)

type LsdServer interface {
	Stats() interface{}
	Start()
	Announce(hash string) error
	WriteStatus(io.Writer)
	Close() error
}

type LsdAnnounce struct {
	InfoHashes []string
	Source     netip.AddrPort
}

type LsdServerWrapper struct {
	*bep14.LSP
	logger log.Logger
	cl     *Client
}

func (me LsdServerWrapper) Stats() interface{} {
	// LSD doesn't have stats like DHT, return a simple placeholder
	return map[string]interface{}{
		"status": "running",
	}
}

func (me LsdServerWrapper) Start() {
	go me.LSP.Start()
	go me.consumeAnnouncements()
}

func (me LsdServerWrapper) Announce(hash string) error {
	return me.LSP.Announce([]string{hash})
}

func (me LsdServerWrapper) WriteStatus(w io.Writer) {
	// Write basic LSD status
	w.Write([]byte("LSD Server Status: Running\n"))
}

func (me LsdServerWrapper) Close() error {

	if me.LSP != nil {
		return me.LSP.Close()
	}
	return nil
}

func (me LsdServerWrapper) consumeAnnouncements() {
	defer func() {
		if r := recover(); r != nil {
			me.logger.Levelf(log.Error, "LSD announcement consumer panicked: %v", r)
		}
	}()

	for {
		announce, ok := <-me.LSP.C
		if !ok {
			me.logger.Levelf(log.Info, "LSD announcement channel closed")
			return
		}

		// Process entire announcement under one lock (like DHT)
		me.cl.lock()

		for _, infoHash := range announce.InfoHashes {
			// if me.cl.dopplegangerAddr(announce.Source.String()) {
			// 	continue
			// }

			var infoHashBytes metainfo.Hash
			err := infoHashBytes.FromHexString(infoHash)
			if err != nil {
				me.logger.Levelf(log.Debug, "Failed to decode infohash %s: %v", infoHash, err)
				continue
			}

			t := me.cl.torrent(infoHashBytes)
			if t != nil {
				ip := net.IP(announce.Source.Addr().AsSlice())
				port := int(announce.Source.Port())
				t.addPeers([]Peer{{
					IP:     ip,
					Port:   port,
					Source: peerSourceLsd,
				}})
			}
		}

		me.cl.unlock()
	}
}
