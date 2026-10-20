package covemaster

import (
	"context"
	"net"
	"slices"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

const defaultTunnelPort = 2222

// dialBack serves one OpenTunnel: only allow-listed ports, only 127.0.0.1. It
// opens a Tunnel stream to Jam (same conn and auth metadata as Attach), claims
// the tunnel with open{id}, and pipes bytes until either side closes or ctx
// ends. Closing the local conn is what unblocks the local→Jam read.
func (c *Client) dialBack(ctx context.Context, cc grpc.ClientConnInterface, md metadata.MD, ot *attachpb.OpenTunnel) {
	if !slices.Contains(c.cfg.TunnelPorts, ot.GetPort()) {
		c.log.Warn("tunnel refused: port not allowed", "port", ot.GetPort())
		return
	}
	dial := c.cfg.TunnelDial
	if dial == nil {
		var d net.Dialer
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	}
	local, err := dial(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(ot.GetPort()))))
	if err != nil {
		c.log.Warn("tunnel: local dial failed", "port", ot.GetPort(), "err", err.Error())
		return
	}
	sctx, cancel := context.WithCancel(metadata.NewOutgoingContext(ctx, md))
	defer cancel()
	// Any end of the tunnel (Jam close, Recv error, ctx cancel, return below)
	// cancels sctx, which closes the local conn and unblocks local.Read.
	stop := context.AfterFunc(sctx, func() { _ = local.Close() })
	defer stop()
	defer local.Close()

	stream, err := attachpb.NewRuntimeClient(cc).Tunnel(sctx)
	if err != nil {
		c.log.Warn("tunnel: open stream failed", "err", err.Error())
		return
	}
	if err := stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Open{Open: &attachpb.TunnelOpen{Id: ot.GetId()}}}); err != nil {
		return
	}
	go func() { // Jam → local
		defer cancel()
		for {
			fr, err := stream.Recv()
			if err != nil || fr.GetClose() != nil {
				return
			}
			if _, err := local.Write(fr.GetData()); err != nil {
				return
			}
		}
	}()
	buf := make([]byte, 32<<10) // local → Jam
	for {
		n, err := local.Read(buf)
		if n > 0 {
			if serr := stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Data{Data: append([]byte(nil), buf[:n]...)}}); serr != nil {
				return
			}
		}
		if err != nil {
			if sctx.Err() == nil { // local side ended first: tell Jam
				_ = stream.Send(&attachpb.TunnelFrame{Msg: &attachpb.TunnelFrame_Close{Close: &attachpb.TunnelClose{Reason: "local closed"}}})
				_ = stream.CloseSend()
			}
			return
		}
	}
}
