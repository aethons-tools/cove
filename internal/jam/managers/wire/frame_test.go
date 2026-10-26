package wire

import (
	"bytes"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/managers/managerpb"
)

func TestSplitAssembleRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, MaxFrameData, MaxFrameData + 1, 3*MaxFrameData + 7} {
		data := bytes.Repeat([]byte{'x'}, n)
		for i := range data {
			data[i] = byte(i)
		}
		frames := Split(5, OpRaise, data)
		want := (n + MaxFrameData - 1) / MaxFrameData
		if want < 1 {
			want = 1
		}
		if len(frames) != want {
			t.Fatalf("n=%d: %d frames, want %d", n, len(frames), want)
		}
		var a Assembler
		for i, f := range frames {
			if len(f.Data) > MaxFrameData || f.Id != 5 || f.Op != OpRaise {
				t.Fatalf("n=%d frame %d malformed", n, i)
			}
			last := i == len(frames)-1
			if f.More == last {
				t.Fatalf("n=%d frame %d: more=%v", n, i, f.More)
			}
			got, done, err := a.Add(f)
			if err != nil || done != last {
				t.Fatalf("n=%d frame %d: done=%v err=%v", n, i, done, err)
			}
			if last && !bytes.Equal(got, data) {
				t.Fatalf("n=%d: data mismatch", n)
			}
		}
	}
}

func TestAssemblerInterleavedIDs(t *testing.T) {
	a, b := bytes.Repeat([]byte("a"), MaxFrameData+5), bytes.Repeat([]byte("b"), MaxFrameData+9)
	fa, fb := Split(1, OpProbe, a), Split(2, OpProbe, b)
	var ra, rb []byte
	var asm Assembler
	for i := 0; i < 2; i++ {
		if d, done, err := asm.Add(fa[i]); err != nil {
			t.Fatal(err)
		} else if done {
			ra = d
		}
		if d, done, err := asm.Add(fb[i]); err != nil {
			t.Fatal(err)
		} else if done {
			rb = d
		}
	}
	if !bytes.Equal(ra, a) || !bytes.Equal(rb, b) {
		t.Fatal("interleaved reassembly mismatch")
	}
}

func TestAssemblerLogFramePassthrough(t *testing.T) {
	var a Assembler
	d, done, err := a.Add(&managerpb.Frame{Id: 9, Op: OpLog, Data: []byte("chunk"), More: true})
	if err != nil || !done || string(d) != "chunk" {
		t.Fatalf("log frame: %q %v %v", d, done, err)
	}
}

func TestAssemblerCap(t *testing.T) {
	a := Assembler{Max: 10}
	if _, _, err := a.Add(&managerpb.Frame{Id: 1, Op: OpRaise, Data: make([]byte, 6), More: true}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Add(&managerpb.Frame{Id: 1, Op: OpRaise, Data: make([]byte, 6), More: true}); err == nil {
		t.Fatal("expected cap error")
	}
}

func TestAssemblerDrop(t *testing.T) {
	var a Assembler
	a.Drop(7) // nothing buffered: a no-op
	if _, _, err := a.Add(&managerpb.Frame{Id: 7, Op: OpResult, Data: []byte("stale"), More: true}); err != nil {
		t.Fatal(err)
	}
	if n := a.Buffered(); n != 1 {
		t.Fatalf("Buffered = %d, want 1", n)
	}
	a.Drop(7)
	if n := a.Buffered(); n != 0 {
		t.Fatalf("Buffered after Drop = %d, want 0", n)
	}
	d, done, err := a.Add(&managerpb.Frame{Id: 7, Op: OpResult, Data: []byte("fresh")})
	if err != nil || !done || string(d) != "fresh" {
		t.Fatalf("after Drop: %q %v %v", d, done, err)
	}
}
