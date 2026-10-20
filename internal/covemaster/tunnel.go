package covemaster

import (
	"context"
	"net"
	"slices"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/aethons-tools/cove/internal/jam/attach/attachpb"
)

const defaultTunnelPort = 2222

// tunnelDrainTimeout bounds how long a tunnel whose local side closed waits
// for Jam to take the last frames and end the stream.
const tunnelDrainTimeout = 5 * time.Second

// setTunnelPorts replaces (not merges) the dynamic part of the allow-list.
func (c *Client) setTunnelPorts(ports []int32) {
	c.tunnelMu.Lock()
	c.tunnelPorts = slices.Clone(ports)
	c.tunnelMu.Unlock()
}

// allowed reports whether Jam may open a tunnel to port: sshd's port always,
// plus Config.TunnelPorts, plus the latest SetTunnelPorts from Jam.
func (c *Client) allowed(port int32) bool {
	if port == defaultTunnelPort || slices.Contains(c.cfg.TunnelPorts, port) {
		return true
	}
	c.tunnelMu.Lock()
	defer c.tunnelMu.Unlock()
	return slices.Contains(c.tunnelPorts, port)
}

// dialBack serves one OpenTunnel: only allow-listed ports, only 127.0.0.1. It
// opens a Tunnel stream to Jam (same conn and auth metadata as Attach), claims
// the tunnel with open{id}, and pipes bytes until either side closes or ctx
// ends. Closing the local conn is what unblocks the local→Jam read; when the
// local side closes first, the stream stays up until Jam has drained it.
func (c *Client) dialBack(ctx context.Context, cc grpc.ClientConnInterface, md metadata.MD, ot *attachpb.OpenTunnel) {
	if !c.allowed(ot.GetPort()) {
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
	recvDone := make(chan struct{})
	go func() { // Jam → local, until Jam ends the stream
		defer close(recvDone)
		defer cancel()
		for {
			fr, err := stream.Recv()
			if err != nil || fr.GetClose() != nil {
				return
			}
			if _, err := local.Write(fr.GetData()); err != nil {
				// Local is gone: close it so the read loop below tells Jam,
				// but keep receiving so the stream is not cancelled before
				// Jam has the tail.
				_ = local.Close()
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
				// Cancelling the stream now could discard frames still
				// queued for Jam (sshd's last bytes, its exit status): wait,
				// bounded, for Jam to end the stream first.
				t := time.NewTimer(tunnelDrainTimeout)
				defer t.Stop()
				select {
				case <-recvDone:
				case <-t.C:
				}
			}
			return
		}
	}
}
