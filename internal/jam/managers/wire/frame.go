package wire

import (
	"fmt"

	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
)

// Split chunks data into frames of at most MaxFrameData bytes. It always
// returns at least one frame; every frame but the last has More set.
func Split(id uint64, op string, data []byte) []*managerpb.Frame {
	n := (len(data) + MaxFrameData - 1) / MaxFrameData
	if n < 1 {
		n = 1
	}
	out := make([]*managerpb.Frame, 0, n)
	for i := 0; i < n; i++ {
		lo := i * MaxFrameData
		hi := min(lo+MaxFrameData, len(data))
		out = append(out, &managerpb.Frame{Id: id, Op: op, Data: data[lo:hi], More: i < n-1})
	}
	return out
}

// Assembler reassembles chunked frames, keyed by id. The zero value is usable.
// It is not safe for concurrent use.
type Assembler struct {
	max  int // test override for MaxMessageBytes
	bufs map[uint64][]byte
}

// Add feeds one frame. When it completes a message, done is true and data is
// the whole payload. An OpLog frame is returned as is. Exceeding the size cap
// for one id is an error and drops that id's partial data.
func (a *Assembler) Add(f *managerpb.Frame) (data []byte, done bool, err error) {
	if f.Op == OpLog {
		return f.Data, true, nil
	}
	limit := a.max
	if limit == 0 {
		limit = MaxMessageBytes
	}
	prev := a.bufs[f.Id]
	if len(prev)+len(f.Data) > limit {
		delete(a.bufs, f.Id)
		return nil, false, fmt.Errorf("wire: message %d exceeds %d bytes", f.Id, limit)
	}
	if f.More {
		if a.bufs == nil {
			a.bufs = map[uint64][]byte{}
		}
		a.bufs[f.Id] = append(prev, f.Data...)
		return nil, false, nil
	}
	delete(a.bufs, f.Id)
	if prev == nil {
		return f.Data, true, nil
	}
	return append(prev, f.Data...), true, nil
}
