package jsonl

import (
	"bufio"
	"context"
	"io"
	"time"
)

func Follow(ctx context.Context, r io.Reader, poll time.Duration, emit func(line []byte)) error {
	br := bufio.NewReader(r)

	var pending []byte
	for {
		chunk, err := br.ReadBytes('\n')

		if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
			emit(append(pending, chunk[:len(chunk)-1]...))
			pending = nil
		} else {
			pending = append(pending, chunk...)
		}

		if err == io.EOF {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(poll):
			}
		} else if err != nil {
			return err
		}
	}
}
