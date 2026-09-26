package jsonio

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxDocument = 64 << 10

func Decode(r io.Reader, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r, MaxDocument+1))
	if err != nil {
		return err
	}
	if len(b) == 0 || len(b) > MaxDocument {
		return fmt.Errorf("JSON input must be 1..%d bytes", MaxDocument)
	}
	return DecodeBytes(b, dst)
}

func DecodeBytes(b []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func Encode(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}
