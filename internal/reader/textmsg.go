// Package reader — text-message sender.
//
// Sends a TEXT_MESSAGE_APP packet (a regular Meshtastic chat message) to a
// specific destination node. Used by the Misbehaving page's auto-notify
// feature to politely ping nodes that exceed configured packet rate limits
// asking them to review their settings.
package reader

import (
	"fmt"
	"math/rand"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// SendTextMessage sends a direct text message to dest on the given channel.
// channel 0 = primary; that's what the dashboard uses by default because it
// is the only channel whose PSK is shared across the mesh in the standard
// configuration. hopLimit defaults to 3 (Meshtastic stock default) when 0.
//
// Truncates text to 200 bytes to leave headroom inside the ~228-byte
// Meshtastic payload budget. Returns the packet ID assigned to the message
// so the caller can correlate ACK / failure events later.
func (r *Reader) SendTextMessage(dest uint32, text string, channel uint32, hopLimit uint32) (uint32, error) {
	if dest == 0 {
		return 0, fmt.Errorf("dest node is zero")
	}
	if text == "" {
		return 0, fmt.Errorf("text is empty")
	}
	if len(text) > 200 {
		text = text[:200]
	}
	if hopLimit == 0 || hopLimit > 7 {
		hopLimit = 3
	}
	pktID := rand.Uint32() | 1
	pkt := &pb.MeshPacket{
		To:       dest,
		Channel:  channel,
		WantAck:  true,
		Id:       pktID,
		HopLimit: hopLimit,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum: pb.PortNum_TEXT_MESSAGE_APP,
				Payload: []byte(text),
			},
		},
	}
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{Packet: pkt},
	}
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return 0, fmt.Errorf("marshal ToRadio: %w", err)
	}
	if err := r.WriteFrame(data); err != nil {
		return 0, err
	}
	return pktID, nil
}
