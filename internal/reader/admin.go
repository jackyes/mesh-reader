// Package reader — admin helpers.
//
// This file implements functions that send AdminMessage packets to the
// local Meshtastic node to reconfigure it at runtime.  Currently supports
// toggling the DebugLogApiEnabled flag in SecurityConfig so the firmware
// starts streaming LogRecord packets via the FromRadio channel.
package reader

import (
	"fmt"
	"math/rand"

	"google.golang.org/protobuf/proto"

	pb "buf.build/gen/go/meshtastic/protobufs/protocolbuffers/go/meshtastic"
)

// SetDebugLogApi enables or disables the firmware debug log stream on the
// connected node.  When enabled, the firmware sends every debug/info/warn/
// error log line as a FromRadio.LogRecord protobuf message, which our
// decoder already handles as EventLogRecord.
//
// Target = 0 means "local node" (no encryption / admin key required).
func (r *Reader) SetDebugLogApi(enabled bool) error {
	// Build the AdminMessage with a Config.Security.debug_log_api_enabled
	adminMsg := &pb.AdminMessage{
		PayloadVariant: &pb.AdminMessage_SetConfig{
			SetConfig: &pb.Config{
				PayloadVariant: &pb.Config_Security{
					Security: &pb.Config_SecurityConfig{
						DebugLogApiEnabled: enabled,
					},
				},
			},
		},
	}

	adminBytes, err := proto.Marshal(adminMsg)
	if err != nil {
		return fmt.Errorf("marshal AdminMessage: %w", err)
	}

	// Wrap in a MeshPacket addressed to the local node (to=0).
	// want_ack=false, hop_limit=0 (no radio forwarding needed).
	meshPkt := &pb.MeshPacket{
		To:       0, // local node
		WantAck:  false,
		Id:       rand.Uint32() | 1,
		HopLimit: 0,
		PayloadVariant: &pb.MeshPacket_Decoded{
			Decoded: &pb.Data{
				Portnum: pb.PortNum_ADMIN_APP,
				Payload: adminBytes,
			},
		},
	}

	toRadio := &pb.ToRadio{
		PayloadVariant: &pb.ToRadio_Packet{
			Packet: meshPkt,
		},
	}

	data, err := proto.Marshal(toRadio)
	if err != nil {
		return fmt.Errorf("marshal ToRadio: %w", err)
	}
	return r.WriteFrame(data)
}
