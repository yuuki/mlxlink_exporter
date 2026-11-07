package mlxlink

import (
	"bytes"
	"strings"
	"testing"
)

// FuzzLimitedBuffer splits fuzzed output into fuzzed writes against a limit
// near its length. Every write must report success, so os/exec never sees an
// error that would mask the real failure; the buffer must hold exactly the
// first limit bytes, so memory stays bounded; and Exceeded must tell whether
// anything was dropped, so a truncated response is never accepted.
func FuzzLimitedBuffer(f *testing.F) {
	f.Add([]byte(`{"result":{"output":{}}}`), []byte{3, 5}, uint16(8))
	f.Add([]byte(strings.Repeat("a", 64)), []byte{}, uint16(64))
	f.Add([]byte(strings.Repeat("b", 64)), []byte{63}, uint16(65))
	f.Add([]byte("x"), []byte{0}, uint16(0))

	f.Fuzz(func(t *testing.T, output, splits []byte, limitSeed uint16) {
		limit := int(limitSeed) % (2*len(output) + 1)
		buffer := &limitedBuffer{limit: limit}

		rest := output
		for i := 0; len(rest) > 0; i++ {
			// os/exec copies with io.Copy, which never issues an empty
			// write, hence the +1.
			size := len(rest)
			if i < len(splits) {
				size = min(size, int(splits[i])+1)
			}
			n, err := buffer.Write(rest[:size])
			if n != size || err != nil {
				t.Fatalf("Write(%d bytes) = %d, %v; want %d, nil", size, n, err, size)
			}
			rest = rest[size:]
		}

		kept := output[:min(len(output), limit)]
		if !bytes.Equal(buffer.Bytes(), kept) || buffer.String() != string(kept) {
			t.Fatalf("kept %q, want the first %d bytes %q", buffer.Bytes(), limit, kept)
		}
		if got, want := buffer.Exceeded(), len(output) > limit; got != want {
			t.Fatalf("Exceeded() = %t after %d bytes against a limit of %d", got, len(output), limit)
		}
	})
}
