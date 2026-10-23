package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Whiteboards (docs/usage/jam/whiteboards.md): an agent creates and lists
// boards, reads one compactly, applies ops, and renders it to PNG to look
// at. Edits never post to chat or wake anyone.

var boardRef = regexp.MustCompile(`^brd_[0-9a-z]{26}$`)

type boardCreateIn struct {
	Title string `json:"title" jsonschema:"the board's title"`
	To    string `json:"to,omitempty" jsonschema:"optional target, as for send; omit for your own channel"`
}

type boardCreateOut struct {
	BoardID string `json:"board_id"`
	Channel string `json:"channel,omitempty"`
}

type boardsIn struct{}

type boardItem struct {
	BoardID string   `json:"board_id"`
	Channel partyOut `json:"channel"`
	Title   string   `json:"title"`
	Seq     int64    `json:"seq"`
}

type boardsOut struct {
	Boards []boardItem `json:"boards"`
}

type boardIDIn struct {
	BoardID string `json:"board_id" jsonschema:"the board (brd_…)"`
}

type compactEl struct {
	ID    string  `json:"id"`
	Type  string  `json:"type"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Text  string  `json:"text,omitempty"`
	From  string  `json:"from,omitempty"`
	To    string  `json:"to,omitempty"`
	Color string  `json:"color,omitempty"`
	Frame string  `json:"frame,omitempty"`
}

type boardReadOut struct {
	Seq      int64       `json:"seq"`
	Elements []compactEl `json:"elements"`
}

type boardOp struct {
	Op    string   `json:"op" jsonschema:"add, update or delete"`
	Type  string   `json:"type,omitempty" jsonschema:"add: rectangle, ellipse, diamond, text, arrow, line or frame"`
	ID    string   `json:"id,omitempty" jsonschema:"the element's id: required for update/delete; optional for add (pick a readable one to refer to it later)"`
	X     *float64 `json:"x,omitempty"`
	Y     *float64 `json:"y,omitempty"`
	W     *float64 `json:"w,omitempty" jsonschema:"width (shapes size to their text when omitted)"`
	H     *float64 `json:"h,omitempty"`
	Text  string   `json:"text,omitempty" jsonschema:"a shape's or arrow's label, a text element's text, a frame's name"`
	From  string   `json:"from,omitempty" jsonschema:"arrow/line: the id of the shape it starts at"`
	To    string   `json:"to,omitempty" jsonschema:"arrow/line: the id of the shape it ends at"`
	Color string   `json:"color,omitempty" jsonschema:"stroke colour, e.g. #1971c2"`
	Frame string   `json:"frame,omitempty" jsonschema:"the id of a frame to put it in"`
}

type boardApplyIn struct {
	BoardID string    `json:"board_id" jsonschema:"the board (brd_…)"`
	Ops     []boardOp `json:"ops" jsonschema:"changes, applied all or nothing"`
}

type boardApplyOut struct {
	Seq   int64    `json:"seq"`
	Added []string `json:"added"`
}

type boardSnapshotOut struct {
	Path string `json:"path"`
	Seq  string `json:"seq,omitempty"`
}

func checkBoard(id string) error {
	if !boardRef.MatchString(id) {
		return fmt.Errorf("board_id must be a board id (brd_…)")
	}
	return nil
}

func (c *messagingClient) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding Jam %s response", path)
	}
	return nil
}

// postJSONReason posts like postJSON, but surfaces Jam's reason on a 400 (a
// bad board op says which and why).
func (c *messagingClient) postJSONReason(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	body, status, err := c.doStatus(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusBadRequest:
		return fmt.Errorf("Jam refused: %s", trimReason(body))
	case status/100 != 2:
		return fmt.Errorf("Jam messages: unexpected status %d", status)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decoding Jam %s response", path)
		}
	}
	return nil
}

func trimReason(b []byte) string {
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}

func (c *messagingClient) boardSnapshot(ctx context.Context, id string) (boardSnapshotOut, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/squawks/boards/"+id+"/snapshot.png", nil)
	if err != nil {
		return boardSnapshotOut{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := transfers.Do(req)
	if err != nil {
		return boardSnapshotOut{}, fmt.Errorf("board_snapshot: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return boardSnapshotOut{}, fmt.Errorf("board_snapshot: status %d", resp.StatusCode)
	}
	seq := resp.Header.Get("X-Board-Seq")
	dir := filepath.Join(c.home, "boards")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return boardSnapshotOut{}, err
	}
	path := filepath.Join(dir, id+"-"+seq+".png")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return boardSnapshotOut{}, fmt.Errorf("board_snapshot: %s exists (the board hasn't changed since); open it", path)
		}
		return boardSnapshotOut{}, err
	}
	_, err = io.Copy(f, io.LimitReader(resp.Body, 64<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return boardSnapshotOut{}, err
	}
	return boardSnapshotOut{Path: path, Seq: seq}, nil
}

func addBoardTools(s *mcp.Server, client *messagingClient, cfgErr error) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_create",
		Description: "Start a whiteboard in your own channel (or `to`, as for send): it posts a card people open in the Jam app's Inbox. Returns its board_id.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardCreateIn) (*mcp.CallToolResult, boardCreateOut, error) {
		if cfgErr != nil {
			return nil, boardCreateOut{}, cfgErr
		}
		var out boardCreateOut
		return nil, out, client.postJSONReason(ctx, "/squawks/boards", in, &out)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "boards",
		Description: "List the whiteboards in channels you're in (and ones you created).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ boardsIn) (*mcp.CallToolResult, boardsOut, error) {
		if cfgErr != nil {
			return nil, boardsOut{}, cfgErr
		}
		var out boardsOut
		err := client.getJSON(ctx, "/squawks/boards", &out)
		if out.Boards == nil {
			out.Boards = []boardItem{}
		}
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_read",
		Description: "Read a whiteboard as a compact element list: shapes with their labels as text, arrows with the ids they connect (from/to), positions and sizes. Freehand strokes show only their bounds — use board_snapshot to see them.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardIDIn) (*mcp.CallToolResult, boardReadOut, error) {
		if cfgErr != nil {
			return nil, boardReadOut{}, cfgErr
		}
		if err := checkBoard(in.BoardID); err != nil {
			return nil, boardReadOut{}, err
		}
		var out boardReadOut
		err := client.getJSON(ctx, "/squawks/boards/"+in.BoardID+"/read", &out)
		if out.Elements == nil {
			out.Elements = []compactEl{}
		}
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_apply",
		Description: "Change a whiteboard, all or nothing: add (rectangle/ellipse/diamond/text/arrow/line/frame; an arrow with from/to connects two shapes), update (move, resize, relabel, recolour) or delete (also removes its label and connected arrows). Others see it live. Returns the ids added.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardApplyIn) (*mcp.CallToolResult, boardApplyOut, error) {
		if cfgErr != nil {
			return nil, boardApplyOut{}, cfgErr
		}
		if err := checkBoard(in.BoardID); err != nil {
			return nil, boardApplyOut{}, err
		}
		var out boardApplyOut
		return nil, out, client.postJSONReason(ctx, "/squawks/boards/"+in.BoardID+"/apply", struct {
			Ops []boardOp `json:"ops"`
		}{in.Ops}, &out)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "board_snapshot",
		Description: "Render a whiteboard to a PNG in your studio (~/boards/<id>-<seq>.png) and return its path — open it to see the board, freehand drawing included.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardIDIn) (*mcp.CallToolResult, boardSnapshotOut, error) {
		if cfgErr != nil {
			return nil, boardSnapshotOut{}, cfgErr
		}
		if err := checkBoard(in.BoardID); err != nil {
			return nil, boardSnapshotOut{}, err
		}
		out, err := client.boardSnapshot(ctx, in.BoardID)
		return nil, out, err
	})
}
