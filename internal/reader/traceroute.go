// Package reader — traceroute helper.
//
// Sends a TRACEROUTE_APP packet to the requested destination node so the
// firmware initiates a route discovery. The destination radio (and each
// relay along the way) appends itself to the route field; when the
// response comes back our decoder turns it into an EventTraceroute that
// the dashboard already knows how to display.
package reader

import (
	"fmt"
	"math/rand"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// SendTraceroute initiates a Meshtastic traceroute to dest. hopLimit caps
// the number of hops (defaults to 7 if 0). want_response=true tells the
// destination to actually respond with the discovered route.
func (r *Reader) SendTraceroute(dest uint32, hopLimit uint32) error {
	if dest == 0 {
		return fmt.Errorf("dest node is zero")
	}
	if hopLimit == 0 || hopLimit > 7 {
		hopLimit = 7
	}
	rd := &pb.RouteDiscovery{}
	payload, err := proto.Marshal(rd)
	if err != nil {
		return fmt.Errorf("marshal RouteDiscovery: %w", err)
	}
	pkt := &pb.MeshPacket{
		To:       dest,
		WantAck:  true,
		Id:       rand.Uint32() | 1,
		HopLimit: hopLimit,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum:      pb.PortNum_TRACEROUTE_APP,
				Payload:      payload,
				WantResponse: true,
			},
		},
	}
	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{Packet: pkt},
	}
	data, err := proto.Marshal(toRadio)
	if err != nil {
		return fmt.Errorf("marshal ToRadio: %w", err)
	}
	return r.WriteFrame(data)
}
