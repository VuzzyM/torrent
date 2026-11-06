package torrent

import (
	"crypto/sha1"
	"encoding/binary"
	"net"
	"slices"

	"github.com/anacrolix/log"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// defaultAllowedFastK is the number of allowed-fast pieces announced per
// peer. BEP 6 leaves the exact value to the implementation (libtorrent uses
// 10).
const defaultAllowedFastK = 10

// generateFastSet implements the Allowed Fast Set generation algorithm from
// BEP 6. It returns up to k piece indices derived from the peer's IPv4
// address (masked to /24) and the torrent's v1 info hash, so the remote peer
// can independently re-derive and verify the set. IPv6 addresses yield nil,
// as the spec only defines the algorithm for IPv4.
func generateFastSet(k int, numPieces uint32, infoHash [20]byte, ip net.IP) []uint32 {
	if k <= 0 || numPieces == 0 {
		return nil
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}

	ip4 = ip4.Mask(net.CIDRMask(24, 32))

	// x = masked_ip || infohash (4 + 20 bytes)
	x := make([]byte, 24)
	copy(x, ip4)
	copy(x[4:], infoHash[:])

	out := make([]uint32, 0, k)
	h := sha1.New()
	// The outer bound caps work when numPieces is tiny and hashes collide.
	for j := 0; j < k && len(out) < k; j++ {
		h.Reset()
		_, _ = h.Write(x)
		x = h.Sum(x[:0])
		for i := 0; i+4 <= 20 && len(out) < k; i += 4 {
			idx := binary.BigEndian.Uint32(x[i:i+4]) % numPieces
			if !slices.Contains(out, idx) {
				out = append(out, idx)
			}
		}
	}

	return out
}

// sendAllowedFastSet posts one AllowedFast message per piece in the BEP 6
// Allowed Fast Set for the remote peer. It is a no-op without metadata, for
// IPv6 peers, or when the Fast Extension isn't negotiated.
func (cn *connection) sendAllowedFastSet() {
	if !cn.fastEnabled() {
		cn.t.logger.LevelPrint(log.Debug, "BEP6: Fast Extension disabled for peer ", cn.remoteIp())
		return
	}

	if !cn.t.haveInfo() {
		cn.t.logger.LevelPrint(log.Debug, "BEP6: metadata unavailable for peer ", cn.remoteIp())
		return
	}

	numPieces := uint32(cn.t.numPieces())
	if numPieces == 0 {
		cn.t.logger.LevelPrint(log.Debug, "BEP6: no pieces for peer ", cn.remoteIp())
		return
	}

	fastSet := generateFastSet(defaultAllowedFastK, numPieces, cn.t.infoHash, cn.remoteIp())

	cn.t.logger.LevelPrint(
		log.Debug,
		"BEP6: sending AllowedFast to peer ", cn.remoteIp(),
		", pieces: ", fastSet,
	)

	for _, idx := range fastSet {
		cn.Post(pp.Message{
			Type:  pp.AllowedFast,
			Index: pp.Integer(idx),
		})
		torrent.Add("allowed fasts sent", 1)

		cn.t.logger.LevelPrint(
			log.Debug,
			"BEP6: AllowedFast posted to peer ", cn.remoteIp(),
			", piece ", idx,
		)
	}
}
