package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// ReadPartial retains complete records and reports an incomplete or invalid tail.
// A final unterminated JSON object is partial even if its JSON syntax is valid.
func ReadPartial(r io.Reader) (records []json.RawMessage, partial bool, err error) {
	br := bufio.NewReaderSize(io.LimitReader(r, MaxBytes+1), 64<<10)
	var consumed int64
	for {
		var line []byte
		for {
			frag, e := br.ReadSlice('\n')
			consumed += int64(len(frag))
			if consumed > MaxBytes {
				return records, true, fmt.Errorf("evidence exceeds read quota")
			}
			if len(line)+len(frag) > 1<<20 {
				return records, true, fmt.Errorf("event frame exceeds quota")
			}
			line = append(line, frag...)
			if e == bufio.ErrBufferFull {
				continue
			}
			if e == io.EOF {
				if len(line) > 0 {
					return records, true, nil
				}
				return records, false, nil
			}
			if e != nil {
				return records, true, e
			}
			break
		}
		line = bytes.TrimSpace(line)
		if !json.Valid(line) {
			return records, true, fmt.Errorf("invalid event record")
		}
		records = append(records, json.RawMessage(line))
		if len(records) > MaxEvents {
			return records, true, fmt.Errorf("too many records")
		}
	}
}
