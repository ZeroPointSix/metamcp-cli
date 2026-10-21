package script

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// sourceMap is a minimal Source Map v3 reader: enough to map a generated
// position back to an original line for a single-source map.
type sourceMap struct {
	// lines[g] holds the segments of generated line g, sorted by column.
	lines [][]segment
}

type segment struct {
	genCol  int
	srcLine int
	srcCol  int
}

func parseSourceMap(raw []byte) (*sourceMap, error) {
	var m struct {
		Version  int    `json:"version"`
		Mappings string `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Version != 3 {
		return nil, errors.New("unsupported source map version")
	}
	sm := &sourceMap{}
	var srcLine, srcCol int
	for _, group := range strings.Split(m.Mappings, ";") {
		var segs []segment
		genCol := 0
		for _, field := range strings.Split(group, ",") {
			if field == "" {
				continue
			}
			vals, err := decodeVLQ(field)
			if err != nil {
				return nil, err
			}
			genCol += vals[0]
			if len(vals) < 4 {
				continue // segment without a source position
			}
			// vals[1] is the source index; esbuild emits a single source.
			srcLine += vals[2]
			srcCol += vals[3]
			segs = append(segs, segment{genCol: genCol, srcLine: srcLine, srcCol: srcCol})
		}
		sort.Slice(segs, func(i, j int) bool { return segs[i].genCol < segs[j].genCol })
		sm.lines = append(sm.lines, segs)
	}
	return sm, nil
}

// lookup maps a 0-based generated line/column to a 0-based source line/column,
// using the closest segment at or before the column.
func (sm *sourceMap) lookup(genLine, genCol int) (int, int, bool) {
	if genLine < 0 || genLine >= len(sm.lines) {
		return 0, 0, false
	}
	segs := sm.lines[genLine]
	if len(segs) == 0 {
		return 0, 0, false
	}
	i := sort.Search(len(segs), func(i int) bool { return segs[i].genCol > genCol }) - 1
	if i < 0 {
		i = 0
	}
	return segs[i].srcLine, segs[i].srcCol, true
}

const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func decodeVLQ(s string) ([]int, error) {
	var (
		out   []int
		value int
		shift uint
	)
	for i := 0; i < len(s); i++ {
		digit := strings.IndexByte(base64Chars, s[i])
		if digit < 0 {
			return nil, errors.New("invalid base64 VLQ digit")
		}
		value += (digit & 31) << shift
		if digit&32 != 0 {
			shift += 5
			continue
		}
		if value&1 != 0 {
			out = append(out, -(value >> 1))
		} else {
			out = append(out, value>>1)
		}
		value, shift = 0, 0
	}
	if shift != 0 {
		return nil, errors.New("truncated VLQ")
	}
	return out, nil
}
